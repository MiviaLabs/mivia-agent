package storage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// The payload writer's refusals, and the one path where the same content
// legitimately arrives twice under a different chunk layout.
//
// A content ref is the SHA-256 of the body, so the same ref must always
// name the same bytes. Every refusal below is what keeps that true when
// two writers disagree - and accepting instead would let one session's
// payload be served under another's reference, which is the one thing a
// content-addressed store must never do.

// payloadFor sanitizes body into a record owned by principal.
func payloadFor(t *testing.T, principal contextstate.Principal, body string) contextstate.PayloadRecord {
	t.Helper()
	san, err := contextstate.SanitizeSourcePayload(context.Background(), principal, []byte(body),
		contextstate.RedactionPolicy{Configured: true, Patterns: []string{"not-present"}})
	if err != nil {
		t.Fatalf("sanitize: %v", err)
	}
	return contextstate.PayloadRecord{
		Ref:       san.Ref,
		Retention: san.Retention,
		Revoked:   san.Revoked,
		Data:      san.Bytes,
	}
}

// inTx runs fn inside a write transaction and rolls back, so each case
// starts from the same store.
func inTx(t *testing.T, s *SQLite, fn func(tx *sql.Tx) error) error {
	t.Helper()
	tx, err := s.beginWrite(context.Background())
	if err != nil {
		t.Fatalf("beginWrite: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	return fn(tx)
}

// TestInsertContextPayloadsRefusesAForeignOwner: the ref carries the
// principal that minted it. A record whose ref belongs to another
// workspace, session or subject must not be written under this
// principal's transaction, or a reference would resolve across the
// boundary it was minted inside.
func TestInsertContextPayloadsRefusesAForeignOwner(t *testing.T) {
	s, principal := openContextTestStore(t)
	defer s.Close()
	seedContextSession(t, s, principal)

	other, err := contextstate.NewPrincipal("other-workspace", "other-session", "other-subject")
	if err != nil {
		t.Fatal(err)
	}
	foreign := payloadFor(t, other, "someone else's body")

	err = inTx(t, s, func(tx *sql.Tx) error {
		_, err := insertContextPayloads(context.Background(), tx, principal, []contextstate.PayloadRecord{foreign})
		return err
	})
	if !errors.Is(err, contextstate.ErrPrincipalMismatch) {
		t.Errorf("a foreign payload was accepted (err=%v), want ErrPrincipalMismatch", err)
	}
}

// TestInsertContextPayloadsRefusesARevokedRecord: revocation is a delete
// that keeps the row. Writing one back would resurrect content the
// retention path already tombstoned.
func TestInsertContextPayloadsRefusesARevokedRecord(t *testing.T) {
	s, principal := openContextTestStore(t)
	defer s.Close()
	seedContextSession(t, s, principal)

	rec := payloadFor(t, principal, "body")
	rec.Revoked = true

	err := inTx(t, s, func(tx *sql.Tx) error {
		_, err := insertContextPayloads(context.Background(), tx, principal, []contextstate.PayloadRecord{rec})
		return err
	})
	if !errors.Is(err, contextstate.ErrPayloadRevoked) {
		t.Errorf("a revoked record was written (err=%v), want ErrPayloadRevoked", err)
	}
}

// TestInsertContextPayloadsRefusesTwoRecordsClaimingOneRef: within a
// single batch the same ref may appear twice only if it is the same
// reference. Two different refs under one key means the caller computed a
// digest over content it then changed.
func TestInsertContextPayloadsRefusesTwoRecordsClaimingOneRef(t *testing.T) {
	s, principal := openContextTestStore(t)
	defer s.Close()
	seedContextSession(t, s, principal)

	first := payloadFor(t, principal, "body")
	second := payloadFor(t, principal, "body")
	second.Ref.Size = first.Ref.Size + 1 // same ref key, different reference

	err := inTx(t, s, func(tx *sql.Tx) error {
		_, err := insertContextPayloads(context.Background(), tx, principal,
			[]contextstate.PayloadRecord{first, second})
		return err
	})
	if err == nil {
		t.Fatal("two different references under one ref key were accepted")
	}
	if !errors.Is(err, contextstate.ErrInvalidDTO) && !errors.Is(err, contextstate.ErrCheckpointConflict) {
		t.Errorf("err = %v, want the duplicate-reference refusal", err)
	}
}

// TestInsertContextPayloadsRefusesARefAlreadyHeldByOtherContent is the
// content-addressing invariant against the STORED row rather than the
// batch: the ref exists with a different digest or size, so the incoming
// record is not what that reference names.
func TestInsertContextPayloadsRefusesARefAlreadyHeldByOtherContent(t *testing.T) {
	s, principal := openContextTestStore(t)
	defer s.Close()
	seedContextSession(t, s, principal)

	rec := payloadFor(t, principal, "the original body")
	if err := inTx(t, s, func(tx *sql.Tx) error {
		if _, err := insertContextPayloads(context.Background(), tx, principal, []contextstate.PayloadRecord{rec}); err != nil {
			return err
		}
		return tx.Commit()
	}); err != nil {
		t.Fatalf("seed write: %v", err)
	}

	// The same ref and the same bytes, re-presented under a different
	// retention class. Validate passes - the digest and size still match
	// the body - so the refusal has to come from the STORED row, which is
	// the arm under test.
	impostor := rec
	if impostor.Retention == contextstate.RetentionCompliance {
		impostor.Retention = contextstate.RetentionSession
	} else {
		impostor.Retention = contextstate.RetentionCompliance
	}

	err := inTx(t, s, func(tx *sql.Tx) error {
		_, err := insertContextPayloads(context.Background(), tx, principal, []contextstate.PayloadRecord{impostor})
		return err
	})
	if !errors.Is(err, contextstate.ErrCheckpointConflict) {
		t.Errorf("a ref held by other content was accepted (err=%v), want ErrCheckpointConflict", err)
	}
}

// TestInsertContextPayloadsSurfacesAFailedWrite: the INSERT is wrapped so
// the caller learns which step failed. Without the wrap a torn batch
// reports a bare driver error and the turn that rolls back has no reason
// attached to it.
func TestInsertContextPayloadsSurfacesAFailedWrite(t *testing.T) {
	s, principal := openContextTestStore(t)
	defer s.Close()
	seedContextSession(t, s, principal)
	failOn(t, s, "fail_payload_insert", "INSERT", "context_payloads")

	rec := payloadFor(t, principal, "body")
	err := inTx(t, s, func(tx *sql.Tx) error {
		_, err := insertContextPayloads(context.Background(), tx, principal, []contextstate.PayloadRecord{rec})
		return err
	})
	if err == nil {
		t.Fatal("a failing INSERT reported success")
	}
	if !strings.Contains(err.Error(), "insert context payload") {
		t.Errorf("error %q does not name the step that failed", err)
	}
}
