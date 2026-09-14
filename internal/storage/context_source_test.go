package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/context/state"
)

func TestSQLiteContextSourceRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, principal := openContextTestStore(t)
	defer s.Close()
	seedContextSession(t, s, principal)

	payload, err := state.SanitizeSourcePayload(ctx, principal, []byte("bounded source"), state.RedactionPolicy{Configured: true, Patterns: []string{"not-present"}})
	if err != nil {
		t.Fatal(err)
	}
	eventID, err := state.NewSourceID(principal.SessionID, 1)
	if err != nil {
		t.Fatal(err)
	}
	event := state.SourceEvent{ID: eventID, Kind: "message", Role: "user", PayloadRef: payload.Ref.Ref, Provenance: "host", RedactionStatus: "sanitized", Size: len(payload.Bytes)}
	record := state.PayloadRecord{Ref: payload.Ref, Retention: payload.Retention, Data: payload.Bytes}
	if err := s.appendSourceEvents(ctx, principal, []state.SourceEvent{event}, []state.PayloadRecord{record}); err != nil {
		t.Fatalf("append source: %v", err)
	}

	rangeID, _ := state.NewSourceID(principal.SessionID, 1)
	rangeEnd, _ := state.NewSourceID(principal.SessionID, 1)
	rng, _ := state.NewSourceRange(rangeID, rangeEnd)
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
	foreign, err := state.NewPrincipal(principal.WorkspaceID, principal.SessionID, "other-subject")
	if err != nil {
		t.Fatal(err)
	}
	id, _ := state.NewSourceID(principal.SessionID, 1)
	rng, _ := state.NewSourceRange(id, id)
	if _, err := s.ReadRange(ctx, foreign, rng); !errors.Is(err, state.ErrPrincipalMismatch) {
		t.Fatalf("foreign range error = %v, want ErrPrincipalMismatch", err)
	}
	ref := state.ContentRef{Ref: "ctxp_missing", Namespace: state.Namespace, SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", WorkspaceID: principal.WorkspaceID, SessionID: principal.SessionID, SubjectID: principal.SubjectID, Size: 4}
	if _, err := s.ReadPayload(ctx, foreign, ref); !errors.Is(err, state.ErrPrincipalMismatch) {
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
	principal, _ := state.NewPrincipal("workspace", "imported", "subject")
	payload, err := state.SanitizeSourcePayload(ctx, principal, []byte("metadata-only"), state.RedactionPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := state.NewSourceID(principal.SessionID, 1)
	event := state.SourceEvent{ID: id, Kind: "message", Role: "user", PayloadRef: payload.Ref.Ref, Provenance: "legacy", RedactionStatus: "hash-only", Size: payload.Ref.Size}
	first, err := s.ImportSource(ctx, principal, "legacy", "import-1", []state.SourceEvent{event}, []state.PayloadRecord{{Ref: payload.Ref, Retention: payload.Retention}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.ImportSource(ctx, principal, "legacy", "import-1", []state.SourceEvent{event}, []state.PayloadRecord{{Ref: payload.Ref, Retention: payload.Retention}})
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
	if _, err := s.ImportSource(ctx, principal, "legacy", "import-1", []state.SourceEvent{event}, []state.PayloadRecord{{Ref: payload.Ref, Retention: payload.Retention}}); err != nil {
		t.Fatalf("reopen idempotent import: %v", err)
	}
	changed := event
	changed.Kind = "different"
	if _, err := s.ImportSource(ctx, principal, "legacy", "import-1", []state.SourceEvent{changed}, []state.PayloadRecord{{Ref: payload.Ref, Retention: payload.Retention}}); !errors.Is(err, state.ErrCheckpointConflict) {
		t.Fatalf("same-key different-content error = %v, want ErrCheckpointConflict", err)
	}
}

func openContextTestStore(t *testing.T) (*SQLite, state.Principal) {
	t.Helper()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	principal, err := state.NewPrincipal("workspace", "session", "subject")
	if err != nil {
		s.Close()
		t.Fatal(err)
	}
	return s, principal
}

func seedContextSession(t *testing.T, s *SQLite, principal state.Principal) {
	t.Helper()
	_, err := s.db.Exec(`INSERT INTO context_sessions(workspace_id,subject_id,session_id,capability_digest,session_revision,durable_revision,source_sequence,provider,model,binding_generation) VALUES(?,?,?,?,0,0,0,'provider','model',1)`, principal.WorkspaceID, principal.SubjectID, principal.SessionID, principal.CapabilityDigest())
	if err != nil {
		t.Fatal(err)
	}
}
