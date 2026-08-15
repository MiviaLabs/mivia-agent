package storage

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// The desktop app's "sessions list" adapter reads each entry's session_id
// and title to label the sidebar row (and to route a later rename). A real
// conversation's shape in the catalog is BOTH a live context_sessions row
// (the session) AND a chat_sessions snapshot under that same id (written
// after every turn by SaveAfterTurn) - and ListSessions' snapshot arm used
// to win that collision with empty session_id/title, shadowing the live
// row: every sidebar entry showed the raw id instead of its title, and the
// desktop's rename could never become visible even though it succeeded.

// listedEntry returns the single listing entry for name, failing the test
// if it is absent or duplicated.
func listedEntry(t *testing.T, store *SQLite, principal contextstate.Principal, name string) contextstate.SessionCatalogInfo {
	t.Helper()
	infos, err := store.ListSessions(context.Background(), principal)
	if err != nil {
		t.Fatal(err)
	}
	var found []contextstate.SessionCatalogInfo
	for _, info := range infos {
		if info.Name == name {
			found = append(found, info)
		}
	}
	if len(found) != 1 {
		t.Fatalf("listing for %q: got %d entries, want exactly 1 (snapshot arm and live arm must not duplicate)", name, len(found))
	}
	return found[0]
}

// TestListSessionsSurfacesLiveRowBehindTurnSnapshot: the snapshot arm must
// carry the live context row's session_id and title when one exists behind
// the snapshot, not empty strings. The direct-API save below declares the
// projection identity via opts.SessionID - the API contract chat's
// SaveAfterTurn satisfies on every turn; a save without that declaration is
// a plain named copy and must list untitled
// (TestListSessionsKeepsPlainSnapshotUntitled).
func TestListSessionsSurfacesLiveRowBehindTurnSnapshot(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, err := contextstate.NewPrincipal("workspace", "LIVESESSIONIDXXXXXXXXXXX", "subject")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSession(context.Background(), contextstate.EnsureSessionRequest{Principal: principal, Binding: mustBinding(t)}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSession(context.Background(), principal, principal.SessionID, []byte(`[{"role":"user","content":"hi"}]`), "model", "provider", 1, 1, 2, contextstate.SessionSaveOptions{SessionID: principal.SessionID}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSessionTitle(context.Background(), principal, principal.SessionID, "my title", contextstate.WorktreeInstance{}); err != nil {
		t.Fatal(err)
	}

	info := listedEntry(t, store, principal, principal.SessionID)
	if info.SessionID != principal.SessionID {
		t.Fatalf("session_id = %q, want the live row's %q (empty shadows the live session: the sidebar shows the raw id and rename looks stuck)", info.SessionID, principal.SessionID)
	}
	if info.Title != "my title" {
		t.Fatalf("title = %q, want the live row's %q", info.Title, "my title")
	}
}

// TestListSessionsKeepsPlainSnapshotUntitled: a snapshot with NO live row
// behind it (a named /save snapshot, or a pruned legacy row) must keep
// listing as before - empty session_id and title, no adoption.
func TestListSessionsKeepsPlainSnapshotUntitled(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, err := contextstate.NewPrincipal("workspace", "session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSession(context.Background(), principal, "named-snapshot", []byte(`[{"role":"user","content":"hi"}]`), "model", "provider", 1, 1, 2, contextstate.SessionSaveOptions{}); err != nil {
		t.Fatal(err)
	}

	info := listedEntry(t, store, principal, "named-snapshot")
	if info.SessionID != "" || info.Title != "" {
		t.Fatalf("plain snapshot entry = {session_id:%q title:%q}, want both empty (no live row exists to surface)", info.SessionID, info.Title)
	}
}
