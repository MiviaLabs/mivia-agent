package cliworkflow

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

func TestOpenWorkflowStoreAdhocHardening(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no chmod permission bits")
	}
	root := t.TempDir()
	res := &config.Resolved{ProviderName: "test"}
	ApplyWorkflowStoreRoot(res, root)

	wantAdhoc := config.TempStorePath(root, "orchestration")
	if res.Subagents.StorePath != wantAdhoc {
		t.Fatalf("res.Subagents.StorePath = %q, want %q", res.Subagents.StorePath, wantAdhoc)
	}

	store, repo, closeFn, err := OpenWorkflowStore(root, res.Subagents)
	if err != nil {
		t.Fatalf("OpenWorkflowStore: %v", err)
	}
	defer closeFn()
	if store == nil || repo == nil {
		t.Fatal("expected non-nil store and repo")
	}

	fi, err := os.Stat(res.Subagents.StorePath)
	if err != nil {
		t.Fatalf("stat store file: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("orchestration store file mode = %o, want 0600", perm)
	}

	dirFi, err := os.Stat(filepath.Dir(res.Subagents.StorePath))
	if err != nil {
		t.Fatalf("stat store dir: %v", err)
	}
	if perm := dirFi.Mode().Perm(); perm != 0o700 {
		t.Errorf("orchestration store dir mode = %o, want 0700", perm)
	}
}

func TestOpenWorkflowStoreOperatorPathNotHardened(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no chmod permission bits")
	}
	root := t.TempDir()
	opPath := filepath.Join(root, "operator.db")
	cfg := config.SubagentConfig{
		StoreBackend: "sqlite",
		StorePath:    opPath,
	}

	store, repo, closeFn, err := OpenWorkflowStore(root, cfg)
	if err != nil {
		t.Fatalf("OpenWorkflowStore: %v", err)
	}
	defer closeFn()
	if store == nil || repo == nil {
		t.Fatal("expected non-nil store and repo")
	}

	fi, err := os.Stat(opPath)
	if err != nil {
		t.Fatalf("stat store file: %v", err)
	}
	// hardening must not touch an operator-configured store: not 0600.
	if perm := fi.Mode().Perm(); perm == 0o600 {
		t.Errorf("operator store file mode = %o, want untouched (hardening leaked)", perm)
	}
}
