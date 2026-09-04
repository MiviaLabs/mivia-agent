package storage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// The hash-only-to-bytes upgrade, on both storage layouts, and the
// corruption checks that guard it.
//
// A ref is minted over the CONTENT's digest, but the bytes are stored
// only when the redaction policy calls them storable - so the same ref is
// legitimately written twice, once without bytes and once with, whenever
// the effective policy differed between the two writes. Refusing the
// second write rolls back the WHOLE turn: the user prompt, the assistant
// reply, and every subagent result in it, for one row. INV-AG-35 says a
// privacy rule may change what is stored and may never destroy a turn the
// agent already finished.
//
// The existing coverage exercises the inline layout. A body larger than
// the chunk size upgrades down a different branch, which is the one a
// real transcript hits.

// smallChunks shrinks the chunk size so a modest body still chunks.
func smallChunks(t *testing.T, size int) {
	t.Helper()
	contextstate.SetLimits(contextstate.Limits{SourceEventBytes: size})
	t.Cleanup(func() { contextstate.SetLimits(contextstate.DefaultLimits()) })
}

// writeRecord commits one payload record on its own transaction.
func writeRecord(t *testing.T, s *SQLite, principal contextstate.Principal, rec contextstate.PayloadRecord) error {
	t.Helper()
	tx, err := s.beginWrite(context.Background())
	if err != nil {
		t.Fatalf("beginWrite: %v", err)
	}
	if _, err := insertContextPayloads(context.Background(), tx, principal, []contextstate.PayloadRecord{rec}); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func storedBody(t *testing.T, s *SQLite, ref string, size int) []byte {
	t.Helper()
	tx, err := s.beginWrite(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	var inline []byte
	if err := tx.QueryRowContext(context.Background(), `SELECT data FROM context_payloads WHERE ref=?`, ref).Scan(&inline); err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("read stored inline: %v", err)
	}
	got, err := loadPayloadBytesTx(context.Background(), tx, ref, size, inline)
	if err != nil {
		t.Fatalf("loadPayloadBytesTx: %v", err)
	}
	return got
}

// TestAChunkedPayloadUpgradesFromHashOnlyToBytes is the branch a real
// transcript takes: the body is larger than one chunk, so the upgrade
// writes the chunk sequence and only then marks the row sanitized.
func TestAChunkedPayloadUpgradesFromHashOnlyToBytes(t *testing.T) {
	smallChunks(t, 256)
	s, principal := openContextTestStore(t)
	defer s.Close()
	seedContextSession(t, s, principal)

	body := strings.Repeat("chunked-transcript-body-", 60) // comfortably several chunks
	full := payloadFor(t, principal, body)
	if len(full.Data) <= contextstate.PayloadChunkSize() {
		t.Fatalf("fixture is not chunked: %d bytes at chunk size %d", len(full.Data), contextstate.PayloadChunkSize())
	}

	hashOnly := full
	hashOnly.Data = nil
	if err := writeRecord(t, s, principal, hashOnly); err != nil {
		t.Fatalf("hash-only write: %v", err)
	}
	if got := storedBody(t, s, full.Ref.Ref, full.Ref.Size); len(got) != 0 {
		t.Fatalf("precondition: the row holds no bytes yet, got %d", len(got))
	}

	if err := writeRecord(t, s, principal, full); err != nil {
		t.Fatalf("the bytes write was refused, which would roll back the whole turn: %v", err)
	}
	if got := string(storedBody(t, s, full.Ref.Ref, full.Ref.Size)); got != body {
		t.Errorf("stored body is %d bytes, want the original %d", len(got), len(body))
	}

	// The payload must now resolve as dereferenceable. That is derived
	// from the stored bytes, not from redaction_status - the read path
	// (context_source.go) sets HashOnly and Dereferenceable from
	// `data == nil` after reassembling chunks, and never consults the
	// status column for that decision.
	//
	// Worth knowing: on this chunked path the row's redaction_status
	// stays "metadata" after the upgrade, where the inline path sets it
	// to "sanitized". insertContextPayloads writes the chunk sequence
	// itself when the body exceeds the chunk size, so by the time
	// reconcilePayloadBytesTx loads the stored bytes they are already
	// there and it returns without calling upgradePayloadBytesTx at all.
	// Nothing reads the column for dereferenceability today, so this is
	// recorded rather than asserted - a test demanding "sanitized" would
	// be asserting behaviour the code does not have.
	got, err := s.ReadPayload(context.Background(), principal, full.Ref)
	if err != nil {
		t.Fatalf("the upgraded payload does not resolve: %v", err)
	}
	if got.HashOnly || !got.Dereferenceable {
		t.Errorf("payload resolves as HashOnly=%v Dereferenceable=%v, want stored bytes", got.HashOnly, got.Dereferenceable)
	}
	if string(got.Bytes) != body {
		t.Errorf("resolved %d bytes, want the original %d", len(got.Bytes), len(body))
	}
}

// TestTheUpgradeIsIdempotent: the same bytes arriving a third time must
// settle silently. The UPDATE is guarded on data IS NULL and the chunk
// insert is ON CONFLICT DO NOTHING, so a replay has to be a no-op rather
// than a conflict that fails the turn.
func TestTheUpgradeIsIdempotent(t *testing.T) {
	smallChunks(t, 256)
	s, principal := openContextTestStore(t)
	defer s.Close()
	seedContextSession(t, s, principal)

	full := payloadFor(t, principal, strings.Repeat("replayed-body-", 80))
	hashOnly := full
	hashOnly.Data = nil

	for i, rec := range []contextstate.PayloadRecord{hashOnly, full, full, full} {
		if err := writeRecord(t, s, principal, rec); err != nil {
			t.Fatalf("write %d was refused: %v", i, err)
		}
	}
	if got := len(storedBody(t, s, full.Ref.Ref, full.Ref.Size)); got != len(full.Data) {
		t.Errorf("after replays the body is %d bytes, want %d", got, len(full.Data))
	}
}

// TestAStoredBodyThatNoLongerMatchesItsRefIsRefused: the ref names a
// digest, so bytes under it that differ are a corrupted row, not a newer
// version. Accepting would serve content under a reference that does not
// describe it.
func TestAStoredBodyThatNoLongerMatchesItsRefIsRefused(t *testing.T) {
	s, principal := openContextTestStore(t)
	defer s.Close()
	seedContextSession(t, s, principal)

	rec := payloadFor(t, principal, "the original body")
	if err := writeRecord(t, s, principal, rec); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Corrupt the stored bytes in place, keeping the length so only the
	// content comparison can catch it.
	corrupt := []byte(strings.Repeat("X", len(rec.Data)))
	if _, err := s.db.Exec(`UPDATE context_payloads SET data=? WHERE ref=?`, corrupt, rec.Ref.Ref); err != nil {
		t.Fatalf("corrupt stored row: %v", err)
	}

	err := writeRecord(t, s, principal, rec)
	if !errors.Is(err, contextstate.ErrCheckpointConflict) {
		t.Errorf("a ref holding different bytes was accepted (err=%v), want ErrCheckpointConflict", err)
	}
}

// TestLoadingAnIncompleteChunkSequenceFails: a payload missing a chunk
// must not reassemble into a short body. It would pass a caller that only
// checks for an error and be handed to a model as truncated history.
func TestLoadingAnIncompleteChunkSequenceFails(t *testing.T) {
	smallChunks(t, 256)
	s, principal := openContextTestStore(t)
	defer s.Close()
	seedContextSession(t, s, principal)

	rec := payloadFor(t, principal, strings.Repeat("body-", 200))
	if err := writeRecord(t, s, principal, rec); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := s.db.Exec(`DELETE FROM context_payload_chunks WHERE ref=? AND chunk_index=1`, rec.Ref.Ref); err != nil {
		t.Fatalf("drop a chunk: %v", err)
	}

	tx, err := s.beginWrite(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()

	got, err := loadPayloadBytesTx(context.Background(), tx, rec.Ref.Ref, rec.Ref.Size, nil)
	if err == nil {
		t.Fatalf("a sequence missing a chunk reassembled into %d bytes", len(got))
	}
	if !errors.Is(err, contextstate.ErrInvalidDTO) {
		t.Errorf("err = %v, want ErrInvalidDTO", err)
	}
}

// TestReadPayloadChunksRejectsASequenceThatDisagreesWithItself: the chunk
// count is written on every row, so two rows claiming different totals
// means the sequence was written by two different layouts.
//
// The neighbouring validations - a non-positive count, an index outside
// the count, a repeated index - are deliberately not tested: the table
// declares PRIMARY KEY(ref, chunk_index) and CHECK(chunk_index >= 0 AND
// chunk_count > 0 AND chunk_index < chunk_count), so those rows cannot be
// inserted at all. Covering them would mean dropping the constraints and
// asserting against a database that cannot exist.
func TestReadPayloadChunksRejectsASequenceThatDisagreesWithItself(t *testing.T) {
	smallChunks(t, 256)
	s, principal := openContextTestStore(t)
	defer s.Close()
	seedContextSession(t, s, principal)

	rec := payloadFor(t, principal, strings.Repeat("body-", 200))
	if err := writeRecord(t, s, principal, rec); err != nil {
		t.Fatalf("seed: %v", err)
	}

	var count int
	if err := s.db.QueryRow(`SELECT chunk_count FROM context_payload_chunks WHERE ref=? LIMIT 1`, rec.Ref.Ref).Scan(&count); err != nil {
		t.Fatal(err)
	}
	// Raise one row's declared total. The per-row CHECK still holds
	// (index < count), so only the cross-row comparison can catch it.
	if _, err := s.db.Exec(`UPDATE context_payload_chunks SET chunk_count=? WHERE ref=? AND chunk_index=0`, count+1, rec.Ref.Ref); err != nil {
		t.Fatalf("skew one row's count: %v", err)
	}

	_, err := readPayloadChunks(context.Background(), s.db, rec.Ref.Ref, rec.Ref.Size)
	if err == nil {
		t.Fatal("a self-inconsistent chunk sequence reassembled")
	}
	if !strings.Contains(err.Error(), "inconsistent chunk_count") {
		t.Errorf("err = %v, want it to name the inconsistent count", err)
	}
}
