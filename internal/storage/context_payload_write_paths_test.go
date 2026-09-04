package storage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// The payload writer's remaining refusals: what it does when the row already
// in the store contradicts the record being written, and what it does when a
// chunk write fails half way through.
//
// A content ref names one body forever. Every arm below is the writer
// refusing to let a second writer redefine that body, or refusing to report
// a write as landed when part of it did not.

// execUnchecked writes a row past the table's CHECK constraints, standing in
// for a row this schema version would not have written - the state these
// reader/writer guards exist for. The pragma is connection-scoped, so the
// write takes a dedicated connection.
func execUnchecked(t *testing.T, s *SQLite, query string, args ...any) {
	t.Helper()
	ctx := context.Background()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `PRAGMA ignore_check_constraints=ON`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, query, args...); err != nil {
		t.Fatalf("seed row: %v", err)
	}
}

// withoutBytes is the same record with its bytes dropped: the reference the
// redaction policy records when the body itself is not storable.
func withoutBytes(rec contextstate.PayloadRecord) contextstate.PayloadRecord {
	rec.Data = nil
	return rec
}

// seedPayloadRow writes the parent payload row for ref directly, with the
// given size, revoked flag and inline body.
func seedPayloadRow(t *testing.T, s *SQLite, principal contextstate.Principal, ref contextstate.ContentRef, size any, revoked int, data []byte) {
	t.Helper()
	execUnchecked(t, s, `INSERT INTO context_payloads(ref,namespace,workspace_id,session_id,subject_id,sha256,size,redaction_status,retention_class,revoked,data) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		ref.Ref, ref.Namespace, ref.WorkspaceID, ref.SessionID, ref.SubjectID, ref.SHA256, size,
		"metadata", string(contextstate.RetentionSession), revoked, data)
}

// TestInsertContextPayloadsRefusesTwoReferencesUnderOneRefKey: two records in
// one batch may share a ref key only when they are the same reference. Here
// both are hash-only (so neither is rejected for its own bytes) and they
// disagree only on size - one of them is describing content it did not
// digest, and accepting either would bind the ref to the wrong length.
func TestInsertContextPayloadsRefusesTwoReferencesUnderOneRefKey(t *testing.T) {
	s, principal := openContextTestStore(t)
	defer s.Close()
	seedContextSession(t, s, principal)

	first := withoutBytes(payloadFor(t, principal, "body"))
	second := first
	second.Ref.Size = first.Ref.Size + 1

	err := inTx(t, s, func(tx *sql.Tx) error {
		_, err := insertContextPayloads(context.Background(), tx, principal,
			[]contextstate.PayloadRecord{first, second})
		return err
	})
	if !errors.Is(err, contextstate.ErrInvalidDTO) {
		t.Fatalf("err = %v, want it to wrap ErrInvalidDTO", err)
	}
	if !strings.Contains(err.Error(), "duplicate payload reference") {
		t.Errorf("err = %q, want it to name the duplicate reference", err.Error())
	}
}

// TestInsertContextPayloadsReportsAnUnreadableStoredRow: the writer reads the
// row back to prove the ref it just claimed is the one it wanted. A row it
// cannot read is not proof of anything, so it must fail rather than fall
// through to the ownership comparison with zero values.
func TestInsertContextPayloadsReportsAnUnreadableStoredRow(t *testing.T) {
	s, principal := openContextTestStore(t)
	defer s.Close()
	seedContextSession(t, s, principal)

	rec := payloadFor(t, principal, "body")
	seedPayloadRow(t, s, principal, rec.Ref, "not-a-number", 0, nil)

	err := inTx(t, s, func(tx *sql.Tx) error {
		_, err := insertContextPayloads(context.Background(), tx, principal, []contextstate.PayloadRecord{rec})
		return err
	})
	if err == nil {
		t.Fatal("a payload row with an unreadable size was accepted")
	}
	if !strings.Contains(err.Error(), "not-a-number") && !strings.Contains(err.Error(), "converting") {
		t.Errorf("err = %v, want the row read failure", err)
	}
}

// TestInsertContextPayloadsRefusesToWriteUnderARevokedRef: the ref is already
// revoked in the store. Writing the record would resurrect content the
// retention path tombstoned, under the reference that recorded the deletion.
func TestInsertContextPayloadsRefusesToWriteUnderARevokedRef(t *testing.T) {
	s, principal := openContextTestStore(t)
	defer s.Close()
	seedContextSession(t, s, principal)

	rec := payloadFor(t, principal, "body")
	seedPayloadRow(t, s, principal, rec.Ref, rec.Ref.Size, 1, nil)

	err := inTx(t, s, func(tx *sql.Tx) error {
		_, err := insertContextPayloads(context.Background(), tx, principal, []contextstate.PayloadRecord{rec})
		return err
	})
	if !errors.Is(err, contextstate.ErrPayloadRevoked) {
		t.Fatalf("err = %v, want ErrPayloadRevoked", err)
	}
}

// TestInsertContextPayloadsReportsAChunkConflict: a chunked body whose ref
// already holds DIFFERENT bytes. The write must fail and say so - the two
// bodies cannot both be the content the ref names, and silently keeping
// either one would serve one session's payload under the other's reference.
func TestInsertContextPayloadsReportsAChunkConflict(t *testing.T) {
	smallChunks(t, 8)
	s, principal := openContextTestStore(t)
	defer s.Close()
	seedContextSession(t, s, principal)

	rec := payloadFor(t, principal, strings.Repeat("body-bytes", 4))
	seedPayloadRow(t, s, principal, rec.Ref, rec.Ref.Size, 0, nil)
	// One stored chunk claiming the whole body, with other bytes in it.
	other := []byte(strings.Repeat("OTHER-BYTE", 4))
	if len(other) != len(rec.Data) {
		t.Fatalf("fixture lengths differ: %d vs %d", len(other), len(rec.Data))
	}
	execUnchecked(t, s, `INSERT INTO context_payload_chunks(ref,chunk_index,chunk_count,data) VALUES(?,0,1,?)`, rec.Ref.Ref, other)

	err := inTx(t, s, func(tx *sql.Tx) error {
		_, err := insertContextPayloads(context.Background(), tx, principal, []contextstate.PayloadRecord{rec})
		return err
	})
	if !errors.Is(err, contextstate.ErrCheckpointConflict) {
		t.Fatalf("err = %v, want ErrCheckpointConflict", err)
	}
	if !strings.Contains(err.Error(), "chunk 0 conflict") {
		t.Errorf("err = %q, want it to name the conflicting chunk", err.Error())
	}
}

// TestInsertContextPayloadsReportsAnUnreadableStoredBody: the stored chunk
// layout disagrees with the incoming one, and the stored rows cannot be
// reassembled either. The writer must report the damage instead of treating
// the unreadable body as "not equal" (a conflict, blaming the caller) or as
// "equal" (accepting, and keeping the damaged rows).
func TestInsertContextPayloadsReportsAnUnreadableStoredBody(t *testing.T) {
	smallChunks(t, 8)
	s, principal := openContextTestStore(t)
	defer s.Close()
	seedContextSession(t, s, principal)

	rec := payloadFor(t, principal, strings.Repeat("body-bytes", 4))
	seedPayloadRow(t, s, principal, rec.Ref, rec.Ref.Size, 0, nil)
	// A single stored chunk that claims to be the whole body but is one byte.
	execUnchecked(t, s, `INSERT INTO context_payload_chunks(ref,chunk_index,chunk_count,data) VALUES(?,0,1,?)`, rec.Ref.Ref, []byte("X"))

	err := inTx(t, s, func(tx *sql.Tx) error {
		_, err := insertContextPayloads(context.Background(), tx, principal, []contextstate.PayloadRecord{rec})
		return err
	})
	if !errors.Is(err, contextstate.ErrInvalidDTO) {
		t.Fatalf("err = %v, want it to wrap ErrInvalidDTO", err)
	}
	if !strings.Contains(err.Error(), "incomplete stored payload") {
		t.Errorf("err = %q, want it to name the unreadable stored body", err.Error())
	}
}

// TestInsertContextPayloadsReportsAnUnreadableStoredBodyOnUpgrade: the row is
// hash-only and the incoming record carries the bytes, so the writer reads
// the stored body to decide between "upgrade this record" and "conflict".
// Stray chunk rows make that read fail, and a failed read must not be
// mistaken for "nothing stored" - that would overwrite whatever is there.
func TestInsertContextPayloadsReportsAnUnreadableStoredBodyOnUpgrade(t *testing.T) {
	s, principal := openContextTestStore(t)
	defer s.Close()
	seedContextSession(t, s, principal)

	rec := payloadFor(t, principal, "small body")
	seedPayloadRow(t, s, principal, rec.Ref, rec.Ref.Size, 0, nil)
	// Two chunks promised, one stored.
	execUnchecked(t, s, `INSERT INTO context_payload_chunks(ref,chunk_index,chunk_count,data) VALUES(?,0,2,?)`, rec.Ref.Ref, []byte("AB"))

	err := inTx(t, s, func(tx *sql.Tx) error {
		_, err := insertContextPayloads(context.Background(), tx, principal, []contextstate.PayloadRecord{rec})
		return err
	})
	if !errors.Is(err, contextstate.ErrInvalidDTO) {
		t.Fatalf("err = %v, want it to wrap ErrInvalidDTO", err)
	}
	if !strings.Contains(err.Error(), "incomplete stored payload") {
		t.Errorf("err = %q, want it to name the unreadable stored body", err.Error())
	}
}

// TestInsertPayloadChunksReportsAFailedWrite: a chunk whose parent payload
// row does not exist violates the foreign key. The writer must report which
// chunk failed rather than return success with the body half written.
func TestInsertPayloadChunksReportsAFailedWrite(t *testing.T) {
	s, principal := openContextTestStore(t)
	defer s.Close()
	seedContextSession(t, s, principal)

	err := inTx(t, s, func(tx *sql.Tx) error {
		return insertPayloadChunks(context.Background(), tx, "no-such-parent-ref", []byte("AB"), 1)
	})
	if err == nil {
		t.Fatal("a chunk with no parent payload row was written")
	}
	if !strings.Contains(err.Error(), "insert payload chunk 0") {
		t.Errorf("err = %q, want it to name the chunk that failed", err.Error())
	}
}

// TestInsertPayloadChunksReportsAnUnreadableStoredChunk: after writing, the
// chunk is read back to prove the stored bytes are the ones intended. A row
// that cannot be read is not that proof.
func TestInsertPayloadChunksReportsAnUnreadableStoredChunk(t *testing.T) {
	s, principal := openContextTestStore(t)
	defer s.Close()
	seedContextSession(t, s, principal)

	rec := payloadFor(t, principal, "body")
	seedPayloadRow(t, s, principal, rec.Ref, rec.Ref.Size, 0, nil)
	execUnchecked(t, s, `INSERT INTO context_payload_chunks(ref,chunk_index,chunk_count,data) VALUES(?,0,?,?)`, rec.Ref.Ref, "not-a-number", []byte("A"))

	err := inTx(t, s, func(tx *sql.Tx) error {
		return insertPayloadChunks(context.Background(), tx, rec.Ref.Ref, []byte("AB"), 1)
	})
	if err == nil {
		t.Fatal("an unreadable stored chunk was accepted as written")
	}
	if !strings.Contains(err.Error(), "not-a-number") && !strings.Contains(err.Error(), "converting") {
		t.Errorf("err = %v, want the chunk read failure", err)
	}
}

// TestInsertPayloadChunksFallsBackToTheDefaultChunkSize: a caller with no
// configured chunk size must not divide the body by zero. The default size
// is large enough that a short body is one chunk.
func TestInsertPayloadChunksFallsBackToTheDefaultChunkSize(t *testing.T) {
	s, principal := openContextTestStore(t)
	defer s.Close()
	seedContextSession(t, s, principal)

	rec := payloadFor(t, principal, "body")
	seedPayloadRow(t, s, principal, rec.Ref, rec.Ref.Size, 0, nil)

	if err := inTx(t, s, func(tx *sql.Tx) error {
		if err := insertPayloadChunks(context.Background(), tx, rec.Ref.Ref, rec.Data, 0); err != nil {
			return err
		}
		var rows, count int
		if err := tx.QueryRow(`SELECT count(*), coalesce(max(chunk_count),0) FROM context_payload_chunks WHERE ref=?`, rec.Ref.Ref).Scan(&rows, &count); err != nil {
			return err
		}
		if rows != 1 || count != 1 {
			t.Errorf("chunk rows=%d chunk_count=%d, want a single chunk at the default size", rows, count)
		}
		body, err := loadPayloadBytesTx(context.Background(), tx, rec.Ref.Ref, len(rec.Data), nil)
		if err != nil {
			return err
		}
		if string(body) != string(rec.Data) {
			t.Errorf("stored body = %q, want %q", body, rec.Data)
		}
		return nil
	}); err != nil {
		t.Fatalf("insert with an unset chunk size: %v", err)
	}
}

// TestInsertPayloadChunksWritesNothingForAnEmptyBody: a hash-only payload has
// no chunks. Writing a zero-length chunk would make the reader report a body
// where the policy stored none.
func TestInsertPayloadChunksWritesNothingForAnEmptyBody(t *testing.T) {
	s, principal := openContextTestStore(t)
	defer s.Close()
	seedContextSession(t, s, principal)

	rec := payloadFor(t, principal, "body")
	seedPayloadRow(t, s, principal, rec.Ref, rec.Ref.Size, 0, nil)

	if err := inTx(t, s, func(tx *sql.Tx) error {
		if err := insertPayloadChunks(context.Background(), tx, rec.Ref.Ref, nil, 4); err != nil {
			return err
		}
		var rows int
		if err := tx.QueryRow(`SELECT count(*) FROM context_payload_chunks WHERE ref=?`, rec.Ref.Ref).Scan(&rows); err != nil {
			return err
		}
		if rows != 0 {
			t.Errorf("an empty body wrote %d chunk rows", rows)
		}
		return nil
	}); err != nil {
		t.Fatalf("insert of an empty body: %v", err)
	}
}

// The hash-only-to-bytes upgrade on the chunked layout, called directly.
//
// insertContextPayloads writes the chunk sequence itself for a body over the
// chunk size, so by the time it reconciles, the bytes are already stored and
// upgradePayloadBytesTx is never reached down that path (see
// context_payload_upgrade_paths_test.go). The function is still the one that
// completes a hash-only row, and its arms are tested here on their own.

// TestUpgradePayloadBytesTxCompletesAChunkedRow: a row first recorded
// hash-only gains its body as a chunk sequence, and only then is marked
// sanitized. Order matters - a row marked sanitized before its chunks landed
// would advertise a body a reader cannot reassemble.
func TestUpgradePayloadBytesTxCompletesAChunkedRow(t *testing.T) {
	s, principal := openContextTestStore(t)
	defer s.Close()
	seedContextSession(t, s, principal)

	rec := payloadFor(t, principal, strings.Repeat("upgrade-me-", 6))
	seedPayloadRow(t, s, principal, rec.Ref, rec.Ref.Size, 0, nil)
	const chunkSize = 8
	if len(rec.Data) <= chunkSize {
		t.Fatalf("fixture of %d bytes does not chunk at %d", len(rec.Data), chunkSize)
	}

	if err := inTx(t, s, func(tx *sql.Tx) error {
		if err := upgradePayloadBytesTx(context.Background(), tx, rec, chunkSize); err != nil {
			return err
		}
		wantChunks := (len(rec.Data) + chunkSize - 1) / chunkSize
		var rows int
		if err := tx.QueryRow(`SELECT count(*) FROM context_payload_chunks WHERE ref=?`, rec.Ref.Ref).Scan(&rows); err != nil {
			return err
		}
		if rows != wantChunks {
			t.Errorf("the upgrade wrote %d chunks, want %d", rows, wantChunks)
		}
		var status string
		if err := tx.QueryRow(`SELECT redaction_status FROM context_payloads WHERE ref=?`, rec.Ref.Ref).Scan(&status); err != nil {
			return err
		}
		if status != "sanitized" {
			t.Errorf("redaction_status = %q, want %q", status, "sanitized")
		}
		body, err := loadPayloadBytesTx(context.Background(), tx, rec.Ref.Ref, rec.Ref.Size, nil)
		if err != nil {
			return err
		}
		if string(body) != string(rec.Data) {
			t.Errorf("upgraded body = %q, want %q", body, rec.Data)
		}
		return nil
	}); err != nil {
		t.Fatalf("chunked upgrade: %v", err)
	}
}

// TestUpgradePayloadBytesTxReportsAFailedChunkWrite: the ref already holds
// different bytes. The upgrade must fail before it marks the row sanitized,
// or the row would claim a body that is not the one the ref names.
func TestUpgradePayloadBytesTxReportsAFailedChunkWrite(t *testing.T) {
	s, principal := openContextTestStore(t)
	defer s.Close()
	seedContextSession(t, s, principal)

	rec := payloadFor(t, principal, strings.Repeat("upgrade-me-", 6))
	seedPayloadRow(t, s, principal, rec.Ref, rec.Ref.Size, 0, nil)
	other := []byte(strings.Repeat("OTHERBYTES-", 6))
	if len(other) != len(rec.Data) {
		t.Fatalf("fixture lengths differ: %d vs %d", len(other), len(rec.Data))
	}
	execUnchecked(t, s, `INSERT INTO context_payload_chunks(ref,chunk_index,chunk_count,data) VALUES(?,0,1,?)`, rec.Ref.Ref, other)

	err := inTx(t, s, func(tx *sql.Tx) error {
		if err := upgradePayloadBytesTx(context.Background(), tx, rec, 8); err != nil {
			return err
		}
		var status string
		if err := tx.QueryRow(`SELECT redaction_status FROM context_payloads WHERE ref=?`, rec.Ref.Ref).Scan(&status); err != nil {
			t.Fatal(err)
		}
		t.Errorf("the upgrade reported success and left the row %q over foreign bytes", status)
		return nil
	})
	if !errors.Is(err, contextstate.ErrCheckpointConflict) {
		t.Fatalf("err = %v, want ErrCheckpointConflict", err)
	}
}

// TestUpgradePayloadBytesTxReportsAFailedStatusUpdate: the chunks landed but
// the row could not be marked. Reporting success would leave the turn
// committed against a row whose status never caught up with its body.
func TestUpgradePayloadBytesTxReportsAFailedStatusUpdate(t *testing.T) {
	for _, tc := range []struct {
		name      string
		body      string
		chunkSize int
		want      string
	}{
		{"chunked layout", strings.Repeat("upgrade-me-", 6), 8, "upgrade chunked payload"},
		{"inline layout", "small body", 4096, "upgrade payload"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, principal := openContextTestStore(t)
			defer s.Close()
			seedContextSession(t, s, principal)

			rec := payloadFor(t, principal, tc.body)
			seedPayloadRow(t, s, principal, rec.Ref, rec.Ref.Size, 0, nil)
			failOn(t, s, "fail_payload_update", "UPDATE", "context_payloads")

			err := inTx(t, s, func(tx *sql.Tx) error {
				return upgradePayloadBytesTx(context.Background(), tx, rec, tc.chunkSize)
			})
			if err == nil {
				t.Fatal("the upgrade reported success with a failing update")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %q, want it to name %q", err.Error(), tc.want)
			}
			if !strings.Contains(err.Error(), "injected") {
				t.Errorf("err = %q, want the underlying failure kept", err.Error())
			}
		})
	}
}
