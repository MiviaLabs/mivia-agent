package clichat

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// TestOpenContextStoreHardensAdhocTempTier pins the production gate itself
// (openContextStore, the opener every workflow ledger open goes through via
// the cliworkflow seam): a store resolved to the ad-hoc temp tier opens
// 0600 inside a 0700 directory chain. The cliworkflow-side replica wiring
// has its own test; this one fails if the implementations drift.
func TestOpenContextStoreHardensAdhocTempTier(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no chmod permission bits")
	}
	root := t.TempDir()
	cfg := config.SubagentConfig{
		StoreBackend: "sqlite",
		StorePath:    config.TempStorePath(root, "orchestration"),
	}

	store, err := openContextStore(root, cfg)
	if err != nil {
		t.Fatalf("openContextStore: %v", err)
	}
	defer store.Close()

	fi, err := os.Stat(cfg.StorePath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("ad-hoc store file mode = %o, want 600", perm)
	}
	dirFi, err := os.Stat(filepath.Dir(cfg.StorePath))
	if err != nil {
		t.Fatal(err)
	}
	if perm := dirFi.Mode().Perm(); perm != 0o700 {
		t.Errorf("ad-hoc store dir mode = %o, want 700", perm)
	}
}

// TestOpenContextStoreLeavesOperatorPathAlone pins the other arm: a
// store_path an operator chose is opened without any chmod.
func TestOpenContextStoreLeavesOperatorPathAlone(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no chmod permission bits")
	}
	root := t.TempDir()
	operatorPath := filepath.Join(root, "operator.db")
	cfg := config.SubagentConfig{
		StoreBackend: "sqlite",
		StorePath:    operatorPath,
	}

	store, err := openContextStore(root, cfg)
	if err != nil {
		t.Fatalf("openContextStore: %v", err)
	}
	defer store.Close()

	fi, err := os.Stat(operatorPath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm == 0o600 {
		t.Errorf("operator store file mode = %o, want untouched (hardening leaked)", perm)
	}
}
