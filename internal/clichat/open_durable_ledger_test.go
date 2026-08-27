package clichat

import (
	"bytes"
	"context"
	cliorchestrate "github.com/MiviaLabs/mivia-agent/internal/cliorchestrate"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

func TestOpenDurableLedgerRepoNonSQLite(t *testing.T) {
	var buf bytes.Buffer
	repo, owned := cliorchestrate.OpenDurableLedgerRepo(config.SubagentConfig{StoreBackend: "memory"}, &buf)
	if repo != *cliorchestrate.DefaultOrchestrationRepo {
		t.Fatal("non-sqlite config must return cliorchestrate.DefaultOrchestrationRepo")
	}
	if owned != nil {
		t.Fatal("non-sqlite config must not own a store")
	}
	if buf.Len() != 0 {
		t.Fatalf("unexpected warning: %q", buf.String())
	}
}

func TestOpenDurableLedgerRepoOpenFailureFallsBack(t *testing.T) {
	var buf bytes.Buffer
	// Make MkdirAll fail: a regular file cannot be a parent directory for the DB.
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.SubagentConfig{
		StoreBackend: "sqlite",
		StorePath:    filepath.Join(blocker, "ledger.db"),
	}
	repo, owned := cliorchestrate.OpenDurableLedgerRepo(cfg, &buf)
	if repo != *cliorchestrate.DefaultOrchestrationRepo {
		t.Fatal("open failure must fall back to cliorchestrate.DefaultOrchestrationRepo")
	}
	if owned != nil {
		t.Fatal("open failure must not return an owned store")
	}
	warn := buf.String()
	if !strings.Contains(warn, "falling back to memory backend") {
		t.Fatalf("warning missing fallback phrase: %q", warn)
	}
	if !strings.Contains(warn, cfg.StorePath) {
		t.Fatalf("warning missing store path: %q", warn)
	}
}

func TestOpenDurableLedgerRepoSuccess(t *testing.T) {
	var buf bytes.Buffer
	path := filepath.Join(t.TempDir(), "orch.db")
	cfg := config.SubagentConfig{StoreBackend: "sqlite", StorePath: path}
	repo, owned := cliorchestrate.OpenDurableLedgerRepo(cfg, &buf)
	if owned == nil {
		t.Fatal("successful open must return owned store")
	}
	if repo != owned {
		t.Fatal("repo and ownedStore must be the same storage repository")
	}
	if _, ok := repo.(*ledger.StorageLedgerRepository); !ok {
		t.Fatalf("repo type = %T, want *ledger.StorageLedgerRepository", repo)
	}
	t.Cleanup(func() { _ = owned.Close() })
	if strings.Contains(buf.String(), "falling back to memory backend") {
		t.Fatalf("success path wrote fallback warning: %q", buf.String())
	}
}

// TestOpenDurableLedgerRepoClosedOnDispatcherClose drives a real constructor
// path: sqlite backend via NewSessionDispatcher (owned store), then
// Dispatcher.Close must close the owned storage repository (subsequent ops
// return ErrClosed).
func TestOpenDurableLedgerRepoClosedOnDispatcherClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "close.db")
	cfg := config.SubagentConfig{
		StoreBackend:   "sqlite",
		StorePath:      path,
		DefaultTimeout: 60,
		MaxWorkers:     1,
	}
	reg := tools.NewRegistry()
	d, err := NewSessionDispatcher(SessionDispatcherOpts{
		Registry:  reg,
		Completer: nullCompleter{},
		Model:     "test-model",
		Config:    cfg,
	})
	if err != nil {
		t.Fatalf("NewSessionDispatcher: %v", err)
	}
	repo := OrchestrationRepoForDispatcher(d)
	sr, ok := repo.(*ledger.StorageLedgerRepository)
	if !ok || sr == nil {
		t.Fatalf("expected StorageLedgerRepository, got %T", repo)
	}
	d.Close()
	// After close, durable ops fail closed.
	if _, err := sr.ListRuns(context.Background()); err == nil {
		t.Fatal("expected error after dispatcher close closed the store")
	}
}
