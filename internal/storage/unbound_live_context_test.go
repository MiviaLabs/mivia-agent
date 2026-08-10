package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

func TestUnboundAPIsRejectManagedLiveContext(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(context.Context, *SQLite, contextstate.Principal) error
	}{
		{"load", func(ctx context.Context, store *SQLite, principal contextstate.Principal) error {
			_, err := store.Load(ctx, principal, principal.SessionID)
			return err
		}},
		{"catalog delete", func(ctx context.Context, store *SQLite, principal contextstate.Principal) error {
			return store.DeleteSessionSnapshot(ctx, principal, principal.SessionID)
		}},
		{"lifecycle delete", func(ctx context.Context, store *SQLite, principal contextstate.Principal) error {
			_, err := store.DeleteSession(ctx, principal, principal.SessionID)
			return err
		}},
		{"export", func(ctx context.Context, store *SQLite, principal contextstate.Principal) error {
			_, err := store.ExportSession(ctx, principal, principal.SessionID)
			return err
		}},
		{"import", func(ctx context.Context, store *SQLite, principal contextstate.Principal) error {
			payload, event := contextSourceFixture(t, principal, "import")
			_, err := store.ImportSource(ctx, principal, "legacy", "operation", []contextstate.SourceEvent{event}, []contextstate.PayloadRecord{{Ref: payload.Ref, Retention: payload.Retention, Data: payload.Bytes}})
			return err
		}},
		{"append source", func(ctx context.Context, store *SQLite, principal contextstate.Principal) error {
			payload, event := contextSourceFixture(t, principal, "append")
			return store.appendSourceEvents(ctx, principal, []contextstate.SourceEvent{event}, []contextstate.PayloadRecord{{Ref: payload.Ref, Retention: payload.Retention, Data: payload.Bytes}})
		}},
		{"read range", func(ctx context.Context, store *SQLite, principal contextstate.Principal) error {
			id, err := contextstate.NewSourceID(principal.SessionID, 1)
			if err != nil {
				return err
			}
			sourceRange, err := contextstate.NewSourceRange(id, id)
			if err != nil {
				return err
			}
			_, err = store.ReadRange(ctx, principal, sourceRange)
			return err
		}},
		{"read payload", func(ctx context.Context, store *SQLite, principal contextstate.Principal) error {
			ref := contextstate.ContentRef{Ref: "ctxp_missing", Namespace: contextstate.Namespace, SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", WorkspaceID: principal.WorkspaceID, SessionID: principal.SessionID, SubjectID: principal.SubjectID, Size: 1}
			_, err := store.ReadPayload(ctx, principal, ref)
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, principal, instance := seedDeletingManagedContext(t)
			defer store.Close()
			ctx := context.Background()
			if _, err := store.LoadWorktree(ctx, principal, principal.SessionID, instance); !errors.Is(err, contextstate.ErrWorktreeDeleted) {
				t.Fatalf("scoped stale load = %v, want ErrWorktreeDeleted", err)
			}
			if err := test.mutate(ctx, store, principal); !errors.Is(err, contextstate.ErrWorktreeDeleted) {
				t.Errorf("unbound operation = %v, want ErrWorktreeDeleted", err)
			}
			assertManagedContextUnchanged(t, store, principal)
		})
	}
}

func seedDeletingManagedContext(t *testing.T) (*SQLite, contextstate.Principal, contextstate.WorktreeInstance) {
	t.Helper()
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	principal := mustCleanupPrincipal(t, "session", "subject")
	instance := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1111111111111111"}
	worktreeDir := filepath.Join(t.TempDir(), "worktrees", instance.Worktree)
	if err := registerCleanupInstance(context.Background(), store, principal, instance, worktreeDir); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.EnsureSession(context.Background(), contextstate.EnsureSessionRequest{Principal: principal, Binding: mustBinding(t), WorktreeInstance: instance}); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.BeginWorktreeDeletion(context.Background(), principal, instance); err != nil {
		store.Close()
		t.Fatal(err)
	}
	return store, principal, instance
}

func assertManagedContextUnchanged(t *testing.T, store *SQLite, principal contextstate.Principal) {
	t.Helper()
	var revision, durable, source, tombstoned int
	if err := store.db.QueryRow(`SELECT session_revision,durable_revision,source_sequence,tombstoned FROM context_sessions WHERE workspace_id=? AND subject_id=? AND session_id=?`, principal.WorkspaceID, principal.SubjectID, principal.SessionID).Scan(&revision, &durable, &source, &tombstoned); err != nil {
		t.Fatal(err)
	}
	if revision != 0 || durable != 0 || source != 0 || tombstoned != 0 {
		t.Fatalf("managed context changed: revision=%d durable=%d source=%d tombstoned=%d", revision, durable, source, tombstoned)
	}
	for _, table := range []string{"context_audits", "context_tombstones", "context_imports", "context_source_events", "context_payloads"} {
		var count int
		if err := store.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s changed: %d rows", table, count)
		}
	}
}
