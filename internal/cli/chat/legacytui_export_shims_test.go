package chat

import (
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// Coverage repair for the legacytui test-export shims the gate flagged: the
// exported wrappers must stay wired to their internal targets, so a fresh
// workspace path still opens through them.

func TestOpenContextStorePathExportOpensStore(t *testing.T) {
	store, err := OpenContextStorePath(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatalf("OpenContextStorePath: %v", err)
	}
	defer store.Close() // release the file handle before t.TempDir cleanup (Windows unlink fails on open files)
	if store == nil {
		t.Fatal("OpenContextStorePath = nil store")
	}
}

func TestOpenContextStoreExportOpensStore(t *testing.T) {
	store, err := OpenContextStore(t.TempDir(), config.SubagentConfig{})
	if err != nil {
		t.Fatalf("OpenContextStore: %v", err)
	}
	defer store.Close()
	if store == nil {
		t.Fatal("OpenContextStore = nil store")
	}
}
