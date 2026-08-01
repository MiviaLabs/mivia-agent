package storage

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

func TestCheckpointRecoveryAfterInjectedFailure(t *testing.T) {
	steps := []string{
		contextFailurePayloadInsert,
		contextFailureSourceAppend,
		contextFailureCheckpointInsert,
		contextFailureCompletionMark,
		contextFailureActivePointerUpdate,
		contextFailureRevisionUpdate,
	}
	for _, step := range steps {
		t.Run(step, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			path := filepath.Join(dir, "context.db")
			s, principal := openContextStoreAt(t, path)
			binding := contextTestBinding(t)
			ensureContextSession(t, s, principal, binding)
			request := contextCommitRequestWithPayload(t, principal, binding)
			s.injectContextFailure(step)
			if err := s.Commit(ctx, request); err == nil {
				t.Fatalf("injected step %q unexpectedly succeeded", step)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err := OpenSQLite(path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			assertEmptyContextHead(t, s, principal)
			if err := s.Commit(ctx, request); err != nil {
				t.Fatalf("retry after %s: %v", step, err)
			}
			snapshot, err := s.Load(ctx, principal, principal.SessionID)
			if err != nil {
				t.Fatalf("load after retry: %v", err)
			}
			if snapshot.Revision != (contextstate.Revision{Session: 1, Durable: 1, Source: 1}) || len(snapshot.Source) != 1 || !snapshot.Active.Complete {
				t.Fatalf("recovered snapshot = %+v", snapshot)
			}
		})
	}
	withSessionCreationFailure(t)
}

func withSessionCreationFailure(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "context.db")
	s, principal := openContextStoreAt(t, path)
	binding := contextTestBinding(t)
	s.injectContextFailure(contextFailureAfterSessionCreation)
	if err := s.EnsureSession(ctx, contextstate.EnsureSessionRequest{Principal: principal, Binding: binding}); err == nil {
		t.Fatal("injected session creation failure unexpectedly succeeded")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.EnsureSession(ctx, contextstate.EnsureSessionRequest{Principal: principal, Binding: binding}); err != nil {
		t.Fatalf("ensure after rollback: %v", err)
	}
	assertEmptyContextHead(t, s, principal)
}

func openContextStoreAt(t *testing.T, path string) (*SQLite, contextstate.Principal) {
	t.Helper()
	s, err := OpenSQLite(path)
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

func contextCommitRequestWithPayload(t *testing.T, principal contextstate.Principal, binding contextstate.BindingRevision) contextstate.CommitRequest {
	t.Helper()
	request := contextCommitRequest(t, principal, contextstate.Revision{}, binding, "commit-with-payload", "payload")
	payload, err := contextstate.SanitizeSourcePayload(context.Background(), principal, []byte("payload"), contextstate.RedactionPolicy{Configured: true, Patterns: []string{"never-match"}})
	if err != nil {
		t.Fatal(err)
	}
	request.NewSourceEvents[0].PayloadRef = payload.Ref.Ref
	request.NewSourceEvents[0].RedactionStatus = "sanitized"
	request.NewSourceEvents[0].Size = payload.Ref.Size
	request.Payloads = []contextstate.PayloadRecord{{Ref: payload.Ref, Retention: payload.Retention, Data: payload.Bytes}}
	request.Fingerprint, err = contextstate.FingerprintCommitRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
	return request
}

func assertEmptyContextHead(t *testing.T, s *SQLite, principal contextstate.Principal) {
	t.Helper()
	snapshot, err := s.Load(context.Background(), principal, principal.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Revision != (contextstate.Revision{}) || len(snapshot.Source) != 0 || snapshot.Active.ID.SessionID != "" {
		t.Fatalf("partial context state = %+v", snapshot)
	}
	var payloads, checkpoints, operations int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM context_payloads`).Scan(&payloads); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM context_checkpoints`).Scan(&checkpoints); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM context_operations`).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if payloads != 0 || checkpoints != 0 || operations != 0 {
		t.Fatalf("orphaned context rows payloads=%d checkpoints=%d operations=%d", payloads, checkpoints, operations)
	}
}
