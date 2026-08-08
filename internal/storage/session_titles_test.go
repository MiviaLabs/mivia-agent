package storage

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

func TestSessionTitlePersistsAndClears(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, err := contextstate.NewPrincipal("workspace", "session-title", "subject")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSession(context.Background(), contextstate.EnsureSessionRequest{Principal: principal, Binding: mustBinding(t)}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSessionTitle(context.Background(), principal, principal.SessionID, "  A title  ", contextstate.WorktreeInstance{}); err != nil {
		t.Fatal(err)
	}
	infos, err := store.ListSessions(context.Background(), principal)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 0 {
		t.Fatalf("empty context session listed: %+v", infos)
	}
	if _, err := store.db.Exec(`UPDATE context_sessions SET source_sequence=1 WHERE workspace_id=? AND session_id=?`, principal.WorkspaceID, principal.SessionID); err != nil {
		t.Fatal(err)
	}
	infos, err = store.ListSessions(context.Background(), principal)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].SessionID != principal.SessionID || infos[0].Title != "A title" {
		t.Fatalf("infos = %+v", infos)
	}
	if err := store.SetSessionTitle(context.Background(), principal, principal.SessionID, " ", contextstate.WorktreeInstance{}); err != nil {
		t.Fatal(err)
	}
	infos, err = store.ListSessions(context.Background(), principal)
	if err != nil {
		t.Fatal(err)
	}
	if infos[0].Title != "" {
		t.Fatalf("cleared title = %q", infos[0].Title)
	}
}

func TestSessionTitleRejectsOtherSubject(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	owner, err := contextstate.NewPrincipal("workspace", "session-owner", "subject")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSession(context.Background(), contextstate.EnsureSessionRequest{Principal: owner, Binding: mustBinding(t)}); err != nil {
		t.Fatal(err)
	}
	other, err := contextstate.NewPrincipal("workspace", "session-other", "other-subject")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetSessionTitle(context.Background(), other, owner.SessionID, "blocked", contextstate.WorktreeInstance{}); err == nil {
		t.Fatal("other subject updated a title")
	}
}

func TestSessionTitleUpdatesLoadedSessionForSameSubject(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	current, err := contextstate.NewPrincipal("workspace", "current", "subject")
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := contextstate.NewPrincipal("workspace", "loaded", "subject")
	if err != nil {
		t.Fatal(err)
	}
	for _, principal := range []contextstate.Principal{current, loaded} {
		if err := store.EnsureSession(context.Background(), contextstate.EnsureSessionRequest{Principal: principal, Binding: mustBinding(t)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SetSessionTitle(context.Background(), current, loaded.SessionID, "Loaded title", contextstate.WorktreeInstance{}); err != nil {
		t.Fatalf("SetSessionTitle loaded session: %v", err)
	}
	var title string
	if err := store.db.QueryRow(`SELECT title FROM context_sessions WHERE workspace_id=? AND session_id=?`, loaded.WorkspaceID, loaded.SessionID).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if title != "Loaded title" {
		t.Fatalf("loaded title = %q", title)
	}
}
