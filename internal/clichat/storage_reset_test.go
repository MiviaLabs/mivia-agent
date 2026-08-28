package clichat

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/hub"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// storageResetWorkspace reuses isolatedSessionsWorkspace's HOME isolation and
// minimal provider config fixture (sessions_command_test.go) - newCatalogSessionAt
// fails config validation without a configured provider, exactly like every
// other command built on it - then seeds one event so a dry run has
// something real to report.
func storageResetWorkspace(t *testing.T) string {
	t.Helper()
	root := isolatedSessionsWorkspace(t)
	store, err := storage.OpenSQLite(filepath.Join(root, ".mivia", "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(context.Background(), storage.Event{ID: "ev-1", RunID: "run", Sequence: 1, Kind: "agent", Payload: []byte(`{"a":1}`)}); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	return root
}

// TestStorageResetDryRunByDefault proves omitting --yes never writes: the
// file's size and mtime are unchanged, and the report still shows the real
// row counts a --yes run would remove.
func TestStorageResetDryRunByDefault(t *testing.T) {
	root := storageResetWorkspace(t)
	dbPath := filepath.Join(root, ".mivia", "context.db")
	before, err := os.Stat(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if err := runStorageWithIO([]string{"reset", "--workspace", root}, &stdout, &stderr); err != nil {
		t.Fatalf("storage reset (dry run): %v", err)
	}
	if !strings.Contains(stdout.String(), "dry run") {
		t.Fatalf("dry-run output missing the dry-run marker:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "would preserve") {
		t.Fatalf("dry-run output missing the preserved-files section:\n%s", stdout.String())
	}

	after, err := os.Stat(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if before.Size() != after.Size() || before.ModTime() != after.ModTime() {
		t.Fatalf("dry run modified the store file: before=%+v after=%+v", before, after)
	}
}

// TestOpenOrchestrationStoreAtHardensTempTier pins the reset command's open
// helper on the ad-hoc tier: the store comes up 0600 inside a 0700 chain.
func TestOpenOrchestrationStoreAtHardensTempTier(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no chmod permission bits")
	}
	root := t.TempDir()
	storePath := config.TempStorePath(root, "orchestration")

	store, err := openOrchestrationStoreAt(root, storePath)
	if err != nil {
		t.Fatalf("openOrchestrationStoreAt: %v", err)
	}
	defer store.Close()

	fi, err := os.Stat(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("ad-hoc orchestration store file mode = %o, want 600", perm)
	}
	dirFi, err := os.Stat(filepath.Dir(storePath))
	if err != nil {
		t.Fatal(err)
	}
	if perm := dirFi.Mode().Perm(); perm != 0o700 {
		t.Errorf("ad-hoc orchestration store dir mode = %o, want 700", perm)
	}
}

// TestOpenOrchestrationStoreAtLeavesOperatorPathAlone pins the other arm: an
// operator-configured store_path opens without any chmod.
func TestOpenOrchestrationStoreAtLeavesOperatorPathAlone(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no chmod permission bits")
	}
	root := t.TempDir()
	operatorPath := filepath.Join(root, "operator.db")

	store, err := openOrchestrationStoreAt(root, operatorPath)
	if err != nil {
		t.Fatalf("openOrchestrationStoreAt: %v", err)
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

// TestStorageResetPreservesMemoryPaths proves the command names the memory
// files it will never touch - the entire point of the feature - in both the
// dry run and the executed --yes path.
func TestStorageResetPreservesMemoryPaths(t *testing.T) {
	root := storageResetWorkspace(t)
	memoryPath := filepath.Join(root, ".mivia", "memory.db")

	var dryOut, dryErr bytes.Buffer
	if err := runStorageWithIO([]string{"reset", "--workspace", root}, &dryOut, &dryErr); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !strings.Contains(dryOut.String(), memoryPath) {
		t.Fatalf("dry-run output does not name %s as preserved:\n%s", memoryPath, dryOut.String())
	}

	var yesOut, yesErr bytes.Buffer
	if err := runStorageWithIO([]string{"reset", "--workspace", root, "--yes"}, &yesOut, &yesErr); err != nil {
		t.Fatalf("storage reset --yes: %v (stderr: %s)", err, yesErr.String())
	}
	if !strings.Contains(yesOut.String(), memoryPath) {
		t.Fatalf("--yes output does not name %s as preserved:\n%s", memoryPath, yesOut.String())
	}
}

// TestStorageResetYesActuallyWipes is the end-to-end proof: after --yes, the
// seeded row is gone and a fresh dry run reports zero rows.
func TestStorageResetYesActuallyWipes(t *testing.T) {
	root := storageResetWorkspace(t)

	var out, errBuf bytes.Buffer
	if err := runStorageWithIO([]string{"reset", "--workspace", root, "--yes"}, &out, &errBuf); err != nil {
		t.Fatalf("storage reset --yes: %v (stderr: %s)", err, errBuf.String())
	}
	dbPath := filepath.Join(root, ".mivia", "context.db")
	if !strings.Contains(out.String(), "wiped "+dbPath) {
		t.Fatalf("output does not confirm %s was wiped:\n%s", dbPath, out.String())
	}

	store, err := storage.OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	n, err := store.Count(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("events count after --yes reset = %d, want 0", n)
	}
}

// TestStorageResetRefusesWhileHubOwned mirrors the --compact refusal: a
// destructive reset must not proceed while an interactive process holds the
// hub lock for the store a live chat session actually joins.
//
// That is the GLOBAL context store's directory (workspace.GlobalContextStorePath,
// under HOME), not the per-workspace orchestration store's directory
// (workspace.ContextStorePath(root)) - the two are legitimately different
// files (see runStorageReset's dedup-by-path logic and orchestrationStorePathFor),
// and hub.Join in production always keys on the store a real chat.Session
// resolves, matching newCatalogSessionAt here. Getting this directory wrong
// would make the refusal check race against the wrong lock entirely.
func TestStorageResetRefusesWhileHubOwned(t *testing.T) {
	root := storageResetWorkspace(t)
	_, contextStore, _, _, err := newCatalogSessionAt(root)
	if err != nil {
		t.Fatal(err)
	}
	hubLockDir := filepath.Dir(contextStore.Path())
	contextStore.Close()

	release, ok := hub.TryAcquireMaintenanceLock(hubLockDir)
	if !ok {
		t.Fatal("could not acquire the hub lock to simulate an owning sibling process")
	}
	defer release()

	var out, errBuf bytes.Buffer
	err = runStorageWithIO([]string{"reset", "--workspace", root, "--yes"}, &out, &errBuf)
	if err == nil {
		t.Fatal("storage reset --yes succeeded while the hub lock was held")
	}
	if !strings.Contains(err.Error(), "another mivia process") {
		t.Fatalf("error = %q, want it to name the reason", err.Error())
	}

	store, err := storage.OpenSQLite(filepath.Join(root, ".mivia", "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	n, err := store.Count(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("events count after a refused reset = %d, want the seeded row (1) untouched", n)
	}
}

// TestStorageResetOneShotCollisionFailsCleanNoPartialWipe locks in the
// residual risk this session's review concluded is safe: a one-shot CLI
// invocation never calls hub.Join, so TryAcquireMaintenanceLock cannot catch
// it (see maintenance_lock.go's doc comment) - but SQLite itself makes the
// collision non-corrupting. A competitor connection left open on the
// per-workspace orchestration store (the second store runStorageReset
// processes; root/.mivia/context.db, matching storageResetWorkspace's seed)
// forces that store's Compact to fail its leave-WAL step for the whole retry
// budget, while never touching the hub lock. The wipe for that store must
// still have committed - "fails clean" means Compact aborts before any
// rewrite, not that the whole reset silently loses the wipe that already
// landed.
func TestStorageResetOneShotCollisionFailsCleanNoPartialWipe(t *testing.T) {
	root := storageResetWorkspace(t)
	orchestrationPath := filepath.Join(root, ".mivia", "context.db")

	competitor, err := storage.OpenSQLite(orchestrationPath)
	if err != nil {
		t.Fatal(err)
	}
	defer competitor.Close()

	var out, errBuf bytes.Buffer
	err = runStorageWithIO([]string{"reset", "--workspace", root, "--yes"}, &out, &errBuf)
	if err == nil {
		t.Fatal("storage reset --yes succeeded despite a one-shot competitor connection open on the orchestration store")
	}
	if !strings.Contains(err.Error(), "safe to retry") {
		t.Fatalf("error = %q, want it to name the collision as safe to retry, not an opaque failure", err.Error())
	}

	competitor.Close()
	reopened, err := storage.OpenSQLite(orchestrationPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	n, err := reopened.Count(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("events count = %d after the wipe step, want 0 - a Compact failure must not roll back a wipe that already committed", n)
	}
	// TableRowCounts re-derives from sqlite_master + a real query per table;
	// a torn or half-rewritten file would fail here rather than reporting a
	// clean, fully-zeroed set - covers what an integrity_check would, without
	// needing an exported query seam.
	counts, err := reopened.TableRowCounts(context.Background())
	if err != nil {
		t.Fatalf("TableRowCounts after a failed compact: %v", err)
	}
	for table, count := range counts {
		if count != 0 {
			t.Fatalf("table %s has %d row(s) after the wipe, want 0", table, count)
		}
	}
}

// TestStorageUnknownSubcommandRejected keeps the dispatch fail-closed.
func TestStorageUnknownSubcommandRejected(t *testing.T) {
	var out, errBuf bytes.Buffer
	if err := runStorageWithIO([]string{"nonsense"}, &out, &errBuf); err == nil {
		t.Fatal("unknown storage subcommand was accepted")
	}
}
