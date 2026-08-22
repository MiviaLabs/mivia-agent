package clichat

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// The tests that used to live here for context_setup.go's worktree-route and
// managed-worktree functions moved to
// internal/cliworktree/context_setup_coverage_test.go along with the
// production code (internal/cliworktree/context_setup.go). This file keeps
// only the tests for what stayed in internal/cli: contextStorePath's
// (ContextStorePath) tilde expansion, the session-context setup/configure
// path, and contextWorkspaceID's symlink canonicalization.

func blockedContextRoot(t *testing.T) string {
	t.Helper()
	root := newWorktreeCommandRepo(t)
	blocker := filepath.Join(root, "blocked")
	if err := os.WriteFile(blocker, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeWorktreeStoreConfig(t, root, filepath.Join(blocker, "context.db"))
	return root
}

func TestContextSetupCoverageOpenErrors(t *testing.T) {
	blocked := blockedContextRoot(t)
	if _, err := OpenRepositoryContextStore(blocked); err == nil {
		t.Fatal("repository store opened through a file")
	}
	res := &config.Resolved{Subagents: config.SubagentConfig{StoreBackend: "sqlite", StorePath: filepath.Join(blocked, "blocked", "store.db")}}
	if _, err := setupSessionContext(chat.NewSession(&config.Resolved{Model: "model"}, nullCompleter{}), blocked, res); err == nil {
		t.Fatal("session store opened through a file")
	}
	if _, err := setupRepositorySessionContext(chat.NewSession(&config.Resolved{Model: "model"}, nullCompleter{}), blocked, blocked, &config.Resolved{}); err == nil {
		t.Fatal("directory opened as a database")
	}
}

func TestContextStorePathExpandsTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.SubagentConfig{StoreBackend: "sqlite", StorePath: "~/.mivia/test-context.db"}
	if got, want := ContextStorePath(t.TempDir(), cfg), filepath.Join(home, ".mivia", "test-context.db"); got != want {
		t.Fatalf("ContextStorePath() = %q, want %q", got, want)
	}
}

func TestContextSetupCoverageConfigureErrorsAndZeroPolicy(t *testing.T) {
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := configureSessionContext(nil, t.TempDir(), store, &config.Resolved{}); err == nil {
		t.Fatal("nil session configuration succeeded")
	}
	if policy := contextRedactionPolicy(nil); policy.Configured {
		t.Fatal("nil redaction policy is configured")
	}
	if policy := contextRedactionPolicy(&config.Resolved{}); policy.Configured {
		t.Fatal("empty redaction policy is configured")
	}
	if err := enableSessionContext(nil, "", nil, nil); err == nil {
		t.Fatal("nil context components succeeded")
	}
	session := chat.NewSession(&config.Resolved{Model: "model"}, nullCompleter{})
	if wiring := contextDispatcherFor(session, config.SubagentConfig{}); wiring.Preparation != nil || wiring.SharedSQLite != nil {
		t.Fatalf("disabled context wiring = %+v", wiring)
	}
}

func TestContextSetupCoverageWorkspaceIdentityCanonicalizesSymlink(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if contextWorkspaceID(real) != contextWorkspaceID(link) {
		t.Fatal("symlink changed workspace identity")
	}
}
