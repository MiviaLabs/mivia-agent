package storage

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// What the payload readers do with a chunk table that does not say what the
// schema says it must.
//
// readPayloadChunks and loadPayloadBytesTx reassemble a body out of rows they
// did not write in this process. The v3 CHECK constraints stop a healthy
// writer from producing the rows below, so every refusal here is the second
// line of defence: a file another process wrote, an older or hand-repaired
// schema, or a read cancelled half way through. What must never happen is a
// SHORT or WRONG body returned as if it were the payload - the caller
// verifies a SHA-256 over what it gets back, but a reader that returns
// (nil, nil) for a broken row set is reported as "hash-only", not as damage.

// openLooseChunkDB is a payload-chunk table WITHOUT the v3 row constraints,
// standing in for a database whose rows the current schema would have
// refused. Only the columns readPayloadChunks/loadPayloadBytesTx read are
// declared.
func openLooseChunkDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "loose.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE context_payload_chunks(
                ref TEXT NOT NULL, chunk_index, chunk_count, data BLOB)`); err != nil {
		t.Fatal(err)
	}
	return db
}

// putLooseChunk writes one row with no constraint checking at all.
func putLooseChunk(t *testing.T, db *sql.DB, ref string, idx, count, data any) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO context_payload_chunks(ref,chunk_index,chunk_count,data) VALUES(?,?,?,?)`, ref, idx, count, data); err != nil {
		t.Fatalf("seed chunk: %v", err)
	}
}

func TestReadPayloadChunksRefusesAMalformedChunkSet(t *testing.T) {
	// Each case is a chunk set that cannot describe a payload, and the
	// message the reader must fail with. All of them wrap ErrInvalidDTO:
	// the stored DTO, not the caller's request, is what is wrong.
	cases := []struct {
		name         string
		expectedSize int
		rows         [][4]any // idx, count, data (ref filled in)
		want         string
	}{
		{
			name: "chunk_count of zero describes no chunks at all",
			rows: [][4]any{{nil, 0, 0, []byte("A")}},
			want: "invalid chunk_count",
		},
		{
			name: "chunk_index past the declared count",
			rows: [][4]any{{nil, 3, 1, []byte("A")}},
			want: "chunk_index out of range",
		},
		{
			name: "negative chunk_index",
			rows: [][4]any{{nil, -1, 2, []byte("A")}},
			want: "chunk_index out of range",
		},
		{
			name: "the same chunk_index twice",
			rows: [][4]any{{nil, 0, 2, []byte("A")}, {nil, 0, 2, []byte("B")}},
			want: "duplicate chunk_index",
		},
		{
			name:         "fewer rows than the count promises",
			expectedSize: 2,
			rows:         [][4]any{{nil, 0, 2, []byte("A")}},
			want:         "incomplete chunk sequence (1 of 2)",
		},
		{
			// A NULL body scans as no bytes: the sequence looks complete and
			// the sizes add up, and only the per-chunk nil check catches it.
			name:         "a hole where a chunk body should be",
			expectedSize: 2,
			rows:         [][4]any{{nil, 0, 2, nil}, {nil, 1, 2, []byte("AB")}},
			want:         "missing chunk 0",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openLooseChunkDB(t)
			for _, row := range tc.rows {
				putLooseChunk(t, db, "ref-1", row[1], row[2], row[3])
			}
			got, err := readPayloadChunks(context.Background(), db, "ref-1", tc.expectedSize)
			if got != nil {
				t.Errorf("a malformed chunk set returned %d bytes; it must return none", len(got))
			}
			if !errors.Is(err, contextstate.ErrInvalidDTO) {
				t.Fatalf("err = %v, want it to wrap ErrInvalidDTO", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %q, want it to name %q", err.Error(), tc.want)
			}
		})
	}
}

// TestReadPayloadChunksReportsAnUnreadableRow: a chunk_index that is not a
// number cannot be ordered or placed. The scan failure must be returned, not
// skipped - skipping the row would silently drop its bytes from the body.
func TestReadPayloadChunksReportsAnUnreadableRow(t *testing.T) {
	db := openLooseChunkDB(t)
	putLooseChunk(t, db, "ref-1", "not-a-number", 1, []byte("A"))

	got, err := readPayloadChunks(context.Background(), db, "ref-1", 1)
	if err == nil {
		t.Fatalf("an unreadable chunk row was accepted, returning %d bytes", len(got))
	}
	if !strings.Contains(err.Error(), "not-a-number") && !strings.Contains(err.Error(), "converting") {
		t.Errorf("err = %v, want the row scan failure", err)
	}
}

// TestReadPayloadChunksReportsAQueryFailure: no chunk table at all is a
// broken store, not an empty payload.
func TestReadPayloadChunksReportsAQueryFailure(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "empty.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	body, err := readPayloadChunks(context.Background(), db, "ref-1", 0)
	if err == nil {
		t.Fatalf("a missing chunk table read as a %d-byte payload", len(body))
	}
	if !strings.Contains(err.Error(), "context_payload_chunks") {
		t.Errorf("err = %v, want the missing-table failure", err)
	}
}

// cancelledQueryer hands back rows whose context is already cancelled, which
// is what a caller abandoning a read looks like from inside the loop: the
// iteration stops early and the error is only visible through rows.Err().
type cancelledQueryer struct {
	t  *testing.T
	db *sql.DB
}

func (c cancelledQueryer) QueryContext(_ context.Context, query string, args ...any) (*sql.Rows, error) {
	c.t.Helper()
	qctx, cancel := context.WithCancel(context.Background())
	rows, err := c.db.QueryContext(qctx, query, args...)
	cancel()
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(5 * time.Second)
	for rows.Err() == nil {
		if time.Now().After(deadline) {
			c.t.Fatal("the cancelled rows never reported their error")
		}
		time.Sleep(time.Millisecond)
	}
	return rows, nil
}

// TestReadPayloadChunksReportsACancelledRead: an iteration that ends because
// the read was cancelled has seen a PREFIX of the chunks. Returning what it
// had (or nil, nil for "hash-only") would hand the caller a truncated body
// under a full payload's reference.
func TestReadPayloadChunksReportsACancelledRead(t *testing.T) {
	db := openLooseChunkDB(t)
	putLooseChunk(t, db, "ref-1", 0, 2, []byte("A"))
	putLooseChunk(t, db, "ref-1", 1, 2, []byte("B"))

	body, err := readPayloadChunks(context.Background(), cancelledQueryer{t: t, db: db}, "ref-1", 2)
	if body != nil {
		t.Errorf("a cancelled read returned %q", body)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// looseTx begins a transaction on the loose chunk table, for the reader that
// takes a *sql.Tx rather than a queryer.
func looseTx(t *testing.T, db *sql.DB) *sql.Tx {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	return tx
}

// TestLoadPayloadBytesTxRefusesABadChunkSequence: the in-transaction reader
// makes the same refusal as readPayloadChunks. It is the one the WRITER uses
// to decide whether a stored body already equals the incoming bytes, so a
// bad sequence read as an empty body would be reported as "equal" and the
// write would silently keep the damaged rows.
func TestLoadPayloadBytesTxRefusesABadChunkSequence(t *testing.T) {
	for _, tc := range []struct {
		name       string
		idx, count any
	}{
		{"index past the count", 4, 2},
		{"negative index", -1, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openLooseChunkDB(t)
			putLooseChunk(t, db, "ref-1", tc.idx, tc.count, []byte("A"))

			body, err := loadPayloadBytesTx(context.Background(), looseTx(t, db), "ref-1", 1, nil)
			if body != nil {
				t.Errorf("a bad chunk sequence returned %q", body)
			}
			if !errors.Is(err, contextstate.ErrInvalidDTO) {
				t.Fatalf("err = %v, want it to wrap ErrInvalidDTO", err)
			}
			if !strings.Contains(err.Error(), "bad chunk sequence") {
				t.Errorf("err = %q, want it to name the bad sequence", err.Error())
			}
		})
	}
}

// TestLoadPayloadBytesTxReportsAnUnreadableRow: same as the reader above -
// an unscannable row is damage, and must not read as "no bytes stored".
//
// The failure must be the READ failure. Letting the scan error through and
// carrying on leaves the row's index and count at zero, which the sequence
// check below then reports as a malformed DTO - the same refusal, blaming
// the wrong thing, and hiding a driver or schema fault behind a data one.
func TestLoadPayloadBytesTxReportsAnUnreadableRow(t *testing.T) {
	db := openLooseChunkDB(t)
	putLooseChunk(t, db, "ref-1", "not-a-number", 1, []byte("A"))

	body, err := loadPayloadBytesTx(context.Background(), looseTx(t, db), "ref-1", 1, nil)
	if err == nil {
		t.Fatalf("an unreadable chunk row was accepted, returning %d bytes", len(body))
	}
	if errors.Is(err, contextstate.ErrInvalidDTO) {
		t.Errorf("err = %v, want the row read failure, not a DTO refusal", err)
	}
	if !strings.Contains(err.Error(), "not-a-number") && !strings.Contains(err.Error(), "converting") {
		t.Errorf("err = %v, want the row scan failure", err)
	}
}

// TestLoadPayloadBytesTxReportsAQueryFailure: with the chunk table dropped
// inside the transaction, the read must fail rather than report an empty
// stored body (which the writer would then "upgrade" over).
func TestLoadPayloadBytesTxReportsAQueryFailure(t *testing.T) {
	db := openLooseChunkDB(t)
	tx := looseTx(t, db)
	if _, err := tx.Exec(`DROP TABLE context_payload_chunks`); err != nil {
		t.Fatal(err)
	}

	body, err := loadPayloadBytesTx(context.Background(), tx, "ref-1", 1, nil)
	if err == nil {
		t.Fatalf("a missing chunk table read as a %d-byte body", len(body))
	}
	if !strings.Contains(err.Error(), "context_payload_chunks") {
		t.Errorf("err = %v, want the missing-table failure", err)
	}
}

// TestLoadPayloadBytesTxReturnsInlineBytesWithoutReading: the inline body of
// a small payload short-circuits the chunk read entirely.
func TestLoadPayloadBytesTxReturnsInlineBytesWithoutReading(t *testing.T) {
	db := openLooseChunkDB(t)
	tx := looseTx(t, db)
	if _, err := tx.Exec(`DROP TABLE context_payload_chunks`); err != nil {
		t.Fatal(err)
	}
	body, err := loadPayloadBytesTx(context.Background(), tx, "ref-1", 2, []byte("hi"))
	if err != nil || string(body) != "hi" {
		t.Fatalf("inline read = (%q, %v), want (\"hi\", nil)", body, err)
	}
}
