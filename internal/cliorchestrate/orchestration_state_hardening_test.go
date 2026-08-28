package cliorchestrate

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// isolateCacheDir points the user cache dir (and HOME, for darwin's
// UserCacheDir) at isolated temp dirs so the default ~/.cache ledger path
// lands somewhere the test can stat, and skips the assertion-bearing tests
// on Windows, which has no chmod permission bits.
func isolateCacheDir(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no chmod permission bits")
	}
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
}

func defaultLedgerPath(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// defaultStorePath resolves against the process working directory at
	// config-load time; the test process never chdirs, so recomputing here
	// names the same file the gate compares against.
	return config.DefaultStorePathForWorkspace(wd)
}

// TestOpenDurableLedgerRepoHardensDefaultCacheStore pins the gate on the
// config-layer default tier: the chat REPL's durable ledger opens 0600
// inside a 0700 directory chain instead of the driver-default 0644/0755.
func TestOpenDurableLedgerRepoHardensDefaultCacheStore(t *testing.T) {
	isolateCacheDir(t)
	storePath := defaultLedgerPath(t)

	var warn bytes.Buffer
	repo, owned := OpenDurableLedgerRepo(config.SubagentConfig{StoreBackend: "sqlite", StorePath: storePath}, &warn)
	if owned == nil {
		t.Fatalf("OpenDurableLedgerRepo returned no owned store (warnings: %s)", warn.String())
	}
	defer owned.Close()
	_ = repo

	fi, err := os.Stat(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("default ledger file mode = %o, want 600", perm)
	}
	dirFi, err := os.Stat(filepath.Dir(storePath))
	if err != nil {
		t.Fatal(err)
	}
	if perm := dirFi.Mode().Perm(); perm != 0o700 {
		t.Errorf("default ledger dir mode = %o, want 700", perm)
	}
}

// TestOpenDurableLedgerRepoLeavesOperatorPathAlone pins the other arm: an
// operator-configured store_path opens without any chmod.
func TestOpenDurableLedgerRepoLeavesOperatorPathAlone(t *testing.T) {
	isolateCacheDir(t)
	operatorPath := filepath.Join(t.TempDir(), "operator.db")

	repo, owned := OpenDurableLedgerRepo(config.SubagentConfig{StoreBackend: "sqlite", StorePath: operatorPath}, &bytes.Buffer{})
	if owned == nil {
		t.Fatal("OpenDurableLedgerRepo returned no owned store for an operator path")
	}
	defer owned.Close()
	_ = repo

	fi, err := os.Stat(operatorPath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm == 0o600 {
		t.Errorf("operator store file mode = %o, want untouched (hardening leaked)", perm)
	}
}

// TestOpenSharedSQLiteHardensDefaultCacheStore pins the shared-store opener
// (the session checkpoint/ledger DB) on the same gate.
func TestOpenSharedSQLiteHardensDefaultCacheStore(t *testing.T) {
	isolateCacheDir(t)
	storePath := defaultLedgerPath(t)

	store, err := OpenSharedSQLite(config.SubagentConfig{StoreBackend: "sqlite", StorePath: storePath}, nil)
	if err != nil {
		t.Fatalf("OpenSharedSQLite: %v", err)
	}
	defer store.Close()

	fi, err := os.Stat(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("default shared store file mode = %o, want 600", perm)
	}
	dirFi, err := os.Stat(filepath.Dir(storePath))
	if err != nil {
		t.Fatal(err)
	}
	if perm := dirFi.Mode().Perm(); perm != 0o700 {
		t.Errorf("default shared store dir mode = %o, want 700", perm)
	}
}
