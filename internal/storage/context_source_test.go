package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

func TestSQLiteContextSourceRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, principal := openContextTestStore(t)
	defer s.Close()
	seedContextSession(t, s, principal)

	payload, err := contextstate.SanitizeSourcePayload(ctx, principal, []byte("bounded source"), contextstate.RedactionPolicy{Configured: true, Patterns: []string{"not-present"}})
	if err != nil {
		t.Fatal(err)
	}
	eventID, err := contextstate.NewSourceID(principal.SessionID, 1)
	if err != nil {
		t.Fatal(err)
	}
	event := contextstate.SourceEvent{ID: eventID, Kind: "message", Role: "user", PayloadRef: payload.Ref.Ref, Provenance: "host", RedactionStatus: "sanitized", Size: len(payload.Bytes)}
	record := contextstate.PayloadRecord{Ref: payload.Ref, Retention: payload.Retention, Data: payload.Bytes}
	if err := s.appendSourceEvents(ctx, principal, []contextstate.SourceEvent{event}, []contextstate.PayloadRecord{record}); err != nil {
		t.Fatalf("append source: %v", err)
	}

	rangeID, _ := contextstate.NewSourceID(principal.SessionID, 1)
	rangeEnd, _ := contextstate.NewSourceID(principal.SessionID, 1)
	rng, _ := contextstate.NewSourceRange(rangeID, rangeEnd)
	gotEvents, err := s.ReadRange(ctx, principal, rng)
	if err != nil {
		t.Fatalf("read range: %v", err)
	}
	if len(gotEvents) != 1 || gotEvents[0].PayloadRef != payload.Ref.Ref {
		t.Fatalf("events = %+v", gotEvents)
	}
	gotPayload, err := s.ReadPayload(ctx, principal, payload.Ref)
	if err != nil {
		t.Fatalf("read payload: %v", err)
	}
	if string(gotPayload.Bytes) != "bounded source" || !gotPayload.Dereferenceable {
		t.Fatalf("payload = %+v", gotPayload)
	}
}

func TestPrincipalScopedReadRangeAndPayload(t *testing.T) {
	ctx := context.Background()
	s, principal := openContextTestStore(t)
	defer s.Close()
	seedContextSession(t, s, principal)
	foreign, err := contextstate.NewPrincipal(principal.WorkspaceID, principal.SessionID, "other-subject")
	if err != nil {
		t.Fatal(err)
	}
	id, _ := contextstate.NewSourceID(principal.SessionID, 1)
	rng, _ := contextstate.NewSourceRange(id, id)
	if _, err := s.ReadRange(ctx, foreign, rng); !errors.Is(err, contextstate.ErrPrincipalMismatch) {
		t.Fatalf("foreign range error = %v, want ErrPrincipalMismatch", err)
	}
	ref := contextstate.ContentRef{Ref: "ctxp_missing", Namespace: contextstate.Namespace, SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", WorkspaceID: principal.WorkspaceID, SessionID: principal.SessionID, SubjectID: principal.SubjectID, Size: 4}
	if _, err := s.ReadPayload(ctx, foreign, ref); !errors.Is(err, contextstate.ErrPrincipalMismatch) {
		t.Fatalf("foreign payload error = %v, want ErrPrincipalMismatch", err)
	}
}

func TestReadPayloadSanitizesAndDeniesForeignPrincipal(t *testing.T) {
	TestPrincipalScopedReadRangeAndPayload(t)
}

func TestSQLiteLegacyImportIsIdempotentAndReopens(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "context.db")
	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	principal, _ := contextstate.NewPrincipal("workspace", "imported", "subject")
	payload, err := contextstate.SanitizeSourcePayload(ctx, principal, []byte("metadata-only"), contextstate.RedactionPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := contextstate.NewSourceID(principal.SessionID, 1)
	event := contextstate.SourceEvent{ID: id, Kind: "message", Role: "user", PayloadRef: payload.Ref.Ref, Provenance: "legacy", RedactionStatus: "hash-only", Size: payload.Ref.Size}
	first, err := s.ImportSource(ctx, principal, "legacy", "import-1", []contextstate.SourceEvent{event}, []contextstate.PayloadRecord{{Ref: payload.Ref, Retention: payload.Retention}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.ImportSource(ctx, principal, "legacy", "import-1", []contextstate.SourceEvent{event}, []contextstate.PayloadRecord{{Ref: payload.Ref, Retention: payload.Retention}})
	if err != nil || second.Rollback.Digest != first.Rollback.Digest {
		t.Fatalf("repeat import = %+v err=%v", second, err)
	}
	var storedData []byte
	if err := s.db.QueryRow(`SELECT data FROM context_payloads WHERE ref=?`, payload.Ref.Ref).Scan(&storedData); err != nil {
		t.Fatal(err)
	}
	if storedData != nil {
		t.Fatalf("unconfigured payload stored bytes: %q", storedData)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.ImportSource(ctx, principal, "legacy", "import-1", []contextstate.SourceEvent{event}, []contextstate.PayloadRecord{{Ref: payload.Ref, Retention: payload.Retention}}); err != nil {
		t.Fatalf("reopen idempotent import: %v", err)
	}
	changed := event
	changed.Kind = "different"
	if _, err := s.ImportSource(ctx, principal, "legacy", "import-1", []contextstate.SourceEvent{changed}, []contextstate.PayloadRecord{{Ref: payload.Ref, Retention: payload.Retention}}); !errors.Is(err, contextstate.ErrCheckpointConflict) {
		t.Fatalf("same-key different-content error = %v, want ErrCheckpointConflict", err)
	}
}

func openContextTestStore(t *testing.T) (*SQLite, contextstate.Principal) {
	t.Helper()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	principal, err := contextstate.NewPrincipal("workspace", "session", "subject")
	if err != nil {
		s.Close()
		t.Fatal(err)
	}
	return s, principal
}

func seedContextSession(t *testing.T, s *SQLite, principal contextstate.Principal) {
	t.Helper()
	_, err := s.db.Exec(`INSERT INTO context_sessions(workspace_id,subject_id,session_id,capability_digest,session_revision,durable_revision,source_sequence,provider,model,binding_generation) VALUES(?,?,?,?,0,0,0,'provider','model',1)`, principal.WorkspaceID, principal.SubjectID, principal.SessionID, principal.CapabilityDigest())
	if err != nil {
		t.Fatal(err)
	}
}
