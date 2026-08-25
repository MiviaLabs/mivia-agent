package clichat

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/hub"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

func TestParseNonNegativeCountRejectsBadInput(t *testing.T) {
	for _, raw := range []string{"", "abc", "-1", "3.5", " 4"} {
		if _, err := parseNonNegativeCount(raw, "--keep"); err == nil {
			t.Fatalf("parseNonNegativeCount(%q) = nil error, want rejection", raw)
		}
	}
	got, err := parseNonNegativeCount("0", "--keep")
	if err != nil || got != 0 {
		t.Fatalf("parseNonNegativeCount(\"0\") = %d, %v; want 0, nil", got, err)
	}
}

// TestSessionsGCRejectsBadFlagValues keeps the verb fail-closed: a malformed
// bound must abort before any store is opened or any row is deleted.
func TestSessionsGCRejectsBadFlagValues(t *testing.T) {
	for _, args := range [][]string{
		{"gc", "--keep-days", "nope"},
		{"gc", "--keep", "-2"},
	} {
		var stdout, stderr bytes.Buffer
		err := runSessionsWithIO(args, &stdout, &stderr)
		if err == nil {
			t.Fatalf("runSessionsWithIO(%v) = nil error, want rejection", args)
		}
		if !strings.Contains(err.Error(), "sessions gc") {
			t.Fatalf("runSessionsWithIO(%v) error = %v, want it scoped to sessions gc", args, err)
		}
	}
}

// TestCompactWithMaintenanceLockRefusesWhileHubOwned proves --compact will
// not attempt the rewrite while another process holds this workspace's hub
// lock (a live TUI/REPL/desktop sidecar). The test stands in for that
// sibling process by acquiring the lock directly, which is exactly what
// hub.Join does for a real one.
func TestCompactWithMaintenanceLockRefusesWhileHubOwned(t *testing.T) {
	dir := t.TempDir()
	store, err := storage.OpenSQLite(filepath.Join(dir, "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	release, ok := hub.TryAcquireMaintenanceLock(filepath.Dir(store.Path()))
	if !ok {
		t.Fatal("could not acquire the hub lock to simulate an owning sibling process")
	}
	defer release()

	var stderr bytes.Buffer
	err = compactWithMaintenanceLock(store, &stderr)
	if err == nil {
		t.Fatal("compactWithMaintenanceLock succeeded while the hub lock was held")
	}
	if !strings.Contains(err.Error(), "another mivia process") {
		t.Fatalf("compactWithMaintenanceLock error = %q, want it to name the reason", err.Error())
	}
	if stderr.Len() == 0 {
		t.Fatal("no message written to stderr on refusal")
	}
}

// TestCompactWithMaintenanceLockSucceedsWhenFree is the common path: no
// sibling process, so --compact runs and releases the lock afterwards.
func TestCompactWithMaintenanceLockSucceedsWhenFree(t *testing.T) {
	dir := t.TempDir()
	store, err := storage.OpenSQLite(filepath.Join(dir, "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	var stderr bytes.Buffer
	if err := compactWithMaintenanceLock(store, &stderr); err != nil {
		t.Fatalf("compactWithMaintenanceLock: %v", err)
	}
	// The lock must be released afterwards, or a second compact in the same
	// process (or the CLI's own next invocation) would wrongly refuse.
	release, ok := hub.TryAcquireMaintenanceLock(filepath.Dir(store.Path()))
	if !ok {
		t.Fatal("hub lock still held after compactWithMaintenanceLock returned")
	}
	release()
}

// TestSessionsUnknownSubcommandStillRejected guards the dispatch edit: adding
// gc must not turn an unknown verb into a silent success.
func TestSessionsUnknownSubcommandStillRejected(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := runSessionsWithIO([]string{"nonsense"}, &stdout, &stderr); err == nil {
		t.Fatal("unknown sessions subcommand was accepted")
	}
}

// TestSessionsNoSubcommandNamesGC keeps the usage line honest.
func TestSessionsNoSubcommandNamesGC(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := runSessionsWithIO(nil, &stdout, &stderr)
	if err == nil {
		t.Fatal("empty sessions invocation was accepted")
	}
	if !strings.Contains(err.Error(), "gc") {
		t.Fatalf("usage error = %v, want it to list gc", err)
	}
}
