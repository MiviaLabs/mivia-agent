package clichat

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

// TestStorageUnknownSubcommandRejected keeps the dispatch fail-closed.
func TestStorageUnknownSubcommandRejected(t *testing.T) {
	var out, errBuf bytes.Buffer
	if err := runStorageWithIO([]string{"nonsense"}, &out, &errBuf); err == nil {
		t.Fatal("unknown storage subcommand was accepted")
	}
}
