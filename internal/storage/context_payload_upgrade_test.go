package storage

// A payload reference is minted over the CONTENT's digest, but the bytes are
// stored only when the redaction policy says they are storable. So the same
// ref can legitimately be written twice: once hash-only (no bytes) and once
// with bytes, whenever the effective policy differs between the two writes -
// a pool-created conversation that inherited no policy, an edited [privacy]
// block, a legacy importer holding a frozen policy.
//
// The reverse order was already tolerated: `if len(payload.Data) > 0` skips
// the comparison entirely when the incoming record has no bytes. Only
// metadata-then-bytes was fatal, and fatal here means the WHOLE turn is rolled
// back - user prompt, assistant reply, and every subagent result in it. That
// is INV-AG-35's rule, which this same package states twice elsewhere: a
// privacy rule may change what is stored, it may never destroy a finished
// turn.

import (
	"context"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// writePayloadRecord appends one source event carrying record, returning the
// error so the caller can assert on it.
func writePayloadRecord(t *testing.T, s *SQLite, principal contextstate.Principal, seq uint64, record contextstate.PayloadRecord, status string) error {
	t.Helper()
	eventID, err := contextstate.NewSourceID(principal.SessionID, seq)
	if err != nil {
		t.Fatal(err)
	}
	event := contextstate.SourceEvent{
		ID: eventID, Kind: "message", Role: "user",
		PayloadRef: record.Ref.Ref, Provenance: "host",
		RedactionStatus: status, Size: record.Ref.Size,
	}
	return s.appendSourceEvents(context.Background(), principal, []contextstate.SourceEvent{event}, []contextstate.PayloadRecord{record})
}

// The exact ordering that wedged a live session: the ref is first recorded
// hash-only, then the same content arrives with bytes.
func TestPayloadHashOnlyThenBytesDoesNotWedgeTheTurn(t *testing.T) {
	s, principal := openContextTestStore(t)
	defer s.Close()
	seedContextSession(t, s, principal)

	body := []byte("a finished turn's content")
	payload, err := contextstate.SanitizeSourcePayload(context.Background(), principal, body,
		contextstate.RedactionPolicy{Configured: true, Patterns: []string{"not-present"}})
	if err != nil {
		t.Fatal(err)
	}

	// First write: the ref, its digest and size, but no bytes - what an
	// unconfigured or non-storable policy produces.
	hashOnly := contextstate.PayloadRecord{Ref: payload.Ref, Retention: payload.Retention}
	if err := writePayloadRecord(t, s, principal, 1, hashOnly, "hash-only"); err != nil {
		t.Fatalf("hash-only write: %v", err)
	}

	// Second write: the same content, now storable.
	withBytes := contextstate.PayloadRecord{Ref: payload.Ref, Retention: payload.Retention, Data: payload.Bytes}
	if err := writePayloadRecord(t, s, principal, 2, withBytes, "sanitized"); err != nil {
		t.Fatalf("a later write of the SAME content must not refuse the turn: %v", err)
	}

	// The upgrade should also be durable: the body is now dereferenceable.
	got, err := s.ReadPayload(context.Background(), principal, payload.Ref)
	if err != nil {
		t.Fatalf("read payload after upgrade: %v", err)
	}
	if string(got.Bytes) != string(payload.Bytes) {
		t.Errorf("payload = %q, want %q", got.Bytes, payload.Bytes)
	}
}

// The reverse order must keep working; it was already tolerated.
func TestPayloadBytesThenHashOnlyStillAccepted(t *testing.T) {
	s, principal := openContextTestStore(t)
	defer s.Close()
	seedContextSession(t, s, principal)

	payload, err := contextstate.SanitizeSourcePayload(context.Background(), principal, []byte("bytes first"),
		contextstate.RedactionPolicy{Configured: true, Patterns: []string{"not-present"}})
	if err != nil {
		t.Fatal(err)
	}
	withBytes := contextstate.PayloadRecord{Ref: payload.Ref, Retention: payload.Retention, Data: payload.Bytes}
	if err := writePayloadRecord(t, s, principal, 1, withBytes, "sanitized"); err != nil {
		t.Fatalf("bytes write: %v", err)
	}
	hashOnly := contextstate.PayloadRecord{Ref: payload.Ref, Retention: payload.Retention}
	if err := writePayloadRecord(t, s, principal, 2, hashOnly, "hash-only"); err != nil {
		t.Fatalf("hash-only after bytes must stay accepted: %v", err)
	}
}
