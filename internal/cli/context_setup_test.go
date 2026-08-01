package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

func TestSetupSessionContextIsAlwaysEnabled(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(filepath.Join(root, ".mivia"), 0o755); err != nil {
		t.Fatal(err)
	}
	session := chat.NewSession(&config.Resolved{Model: "model"}, nullCompleter{})
	store, err := setupSessionContext(session, root, config.DefaultSubagentConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if !session.ContextEnabled() {
		t.Fatal("session context is disabled")
	}
	if _, ok := session.ContextStore().(*storage.SQLite); !ok {
		t.Fatalf("context store = %T, want SQLite", session.ContextStore())
	}
	if _, _, ok := session.ContextPreparation(); !ok {
		t.Fatal("session did not expose isolated preparation capability")
	}
}

func TestSetupSessionContextListsExistingSQLiteContextSessions(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(filepath.Join(root, ".mivia"), 0o755); err != nil {
		t.Fatal(err)
	}
	first := chat.NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, nullCompleter{})
	store, err := setupSessionContext(first, root, config.DefaultSubagentConfig)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.SendUser(context.Background(), "persist this", io.Discard); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()

	second := chat.NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, nullCompleter{})
	store, err = setupSessionContext(second, root, config.DefaultSubagentConfig)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.SendUser(context.Background(), "second session", io.Discard); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()

	loader := chat.NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, nullCompleter{})
	store, err = setupSessionContext(loader, root, config.DefaultSubagentConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	infos, err := loader.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) == 0 {
		t.Fatal("SQLite context session was not presented in session list")
	}
	if len(infos) != 2 {
		t.Fatalf("SQLite context session list = %#v, want two sessions", infos)
	}
	for i, sessionID := range []string{first.SessionID, second.SessionID, first.SessionID, second.SessionID} {
		if err := loader.Load(sessionID); err != nil {
			t.Fatalf("load %d (%s): %v", i, sessionID, err)
		}
	}
	if got := loader.MessagesCopy(); len(got) < 1 || got[0].Content != "second session" {
		t.Fatalf("loaded SQLite context history = %#v", got)
	}
	if err := loader.DeleteSession(first.SessionID); err != nil {
		t.Fatal(err)
	}
	if err := loader.DeleteSession(second.SessionID); err != nil {
		t.Fatal(err)
	}
	if infos, err := loader.ListSessions(); err != nil || len(infos) != 0 {
		t.Fatalf("sessions after delete = %#v, err=%v", infos, err)
	}
}
