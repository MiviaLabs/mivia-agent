package storage

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

func TestSQLiteChatSessionCatalogRoundTripAndPrune(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, err := contextstate.NewPrincipal("workspace", "session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	catalog := contextstate.SessionCatalog(store)
	ctx := context.Background()
	if err := catalog.SaveSession(ctx, principal, "named", []byte(`[{}]`), "model", "provider", 1, 2, 1); err != nil {
		t.Fatal(err)
	}
	data, info, err := catalog.LoadSession(ctx, principal, "named")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `[{}]` || info.Name != "named" || info.MessageCount != 1 {
		t.Fatalf("loaded catalog entry = %q, %+v", data, info)
	}
	list, err := catalog.ListSessions(ctx, principal)
	if err != nil || len(list) != 1 {
		t.Fatalf("listed sessions = %d, err=%v", len(list), err)
	}
	if err := catalog.PruneSessionSnapshots(ctx, principal, []string{"named"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := catalog.LoadSession(ctx, principal, "named"); err != contextstate.ErrSessionNotFound {
		t.Fatalf("load after prune error = %v", err)
	}
}

func TestSQLiteChatSessionCatalogDeleteTombstonesContextSession(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, err := contextstate.NewPrincipal("workspace", "session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := contextstate.NewBindingRevision("provider", "model", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSession(context.Background(), contextstate.EnsureSessionRequest{Principal: principal, Binding: binding}); err != nil {
		t.Fatal(err)
	}
	if err := contextstate.SessionCatalog(store).DeleteSessionSnapshot(context.Background(), principal, principal.SessionID); err != nil {
		t.Fatal(err)
	}
	var tombstoned, audits, tombstones int
	if err := store.db.QueryRow(`SELECT tombstoned FROM context_sessions WHERE session_id=?`, principal.SessionID).Scan(&tombstoned); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM context_audits WHERE session_id=?`, principal.SessionID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM context_tombstones WHERE session_id=?`, principal.SessionID).Scan(&tombstones); err != nil {
		t.Fatal(err)
	}
	if tombstoned != 1 || audits != 1 || tombstones != 1 {
		t.Fatalf("delete lifecycle = tombstoned:%d audits:%d tombstones:%d", tombstoned, audits, tombstones)
	}
}
