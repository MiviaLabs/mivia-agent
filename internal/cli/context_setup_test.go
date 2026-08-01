package cli

import (
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
