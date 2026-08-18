package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// TestSnapshotStore_UniquePrivateStaging is the W-1 regression: the probe
// snapshot must land in a fresh, private (0o700), per-invocation staging
// directory that cleanup removes - never in the shared fixed path
// os.TempDir()/wfprobe that every invocation reused with a world-readable
// directory and 0644 file copies.
func TestSnapshotStore_UniquePrivateStaging(t *testing.T) {
	srcDir := t.TempDir()
	store, err := storage.OpenSQLite(filepath.Join(srcDir, "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(context.Background(), storage.Event{ID: "staging-1", RunID: "staging", Sequence: 1, Kind: "agent", Payload: []byte("safe")}); err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	tmp1, cleanup1, err := snapshotStore(context.Background(), srcDir)
	if err != nil {
		t.Fatal(err)
	}
	tmp2, cleanup2, err := snapshotStore(context.Background(), srcDir)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup2()

	if tmp1 == tmp2 {
		t.Fatalf("two invocations reused the same staging dir %q", tmp1)
	}
	fixed := filepath.Join(os.TempDir(), "wfprobe")
	for _, tmp := range []string{tmp1, tmp2} {
		if tmp == fixed {
			t.Fatalf("snapshot reused the shared fixed staging path %q", fixed)
		}
		info, err := os.Stat(tmp)
		if err != nil {
			t.Fatalf("stat staging dir %q: %v", tmp, err)
		}
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Fatalf("staging dir %q mode = %#o, want 0700", tmp, perm)
		}
	}
	if _, err := os.Stat(fixed); !os.IsNotExist(err) {
		t.Fatalf("shared fixed staging path %q exists after snapshotting, stat err=%v", fixed, err)
	}

	cleanup1()
	if _, err := os.Stat(tmp1); !os.IsNotExist(err) {
		t.Fatalf("cleanup did not remove staging dir %q, stat err=%v", tmp1, err)
	}
	cleanup1() // idempotent
}

// TestSnapshotStore_SelfContainedConsistent is the W-2 regression: the
// snapshot must capture the live store's committed durable state - including
// events that exist only in the uncheckpointed WAL - as a single
// self-contained context.db that passes integrity_check, instead of the old
// torn io.Copy of db+wal+shm taken without any lock.
func TestSnapshotStore_SelfContainedConsistent(t *testing.T) {
	srcDir := t.TempDir()
	store, err := storage.OpenSQLite(filepath.Join(srcDir, "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	const runID = "probe-run"
	for i := 1; i <= 3; i++ {
		if err := store.Append(context.Background(), storage.Event{
			ID:       fmt.Sprintf("probe-%d", i),
			RunID:    runID,
			Sequence: i,
			Kind:     "agent",
			Payload:  []byte(fmt.Sprintf("payload-%d", i)),
		}); err != nil {
			t.Fatal(err)
		}
	}
	// The source store is still open and was never checkpointed: the three
	// committed events live only in context.db-wal at snapshot time.

	tmp, cleanup, err := snapshotStore(context.Background(), srcDir)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "context.db" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("snapshot staging is not a single self-contained context.db, got %v", names)
	}

	raw, err := sql.Open("sqlite", "file:"+filepath.Join(tmp, "context.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	var integrity string
	if err := raw.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	raw.Close()
	if integrity != "ok" {
		t.Fatalf("snapshot integrity_check = %q, want ok", integrity)
	}

	got, err := storage.OpenSQLite(filepath.Join(tmp, "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer got.Close()
	events, err := got.Events(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("snapshot has %d events, want 3 including WAL-only committed events", len(events))
	}
	for i, e := range events {
		if want := fmt.Sprintf("payload-%d", i+1); string(e.Payload) != want {
			t.Fatalf("event %d payload = %q, want %q", i+1, e.Payload, want)
		}
	}
}

// TestSnapshotStore_MissingStoreFailsClean is the negative-path regression: a
// missing context.db must fail hard before any connection opens, so the probe
// can never silently probe an empty store or create one in the source dir.
func TestSnapshotStore_MissingStoreFailsClean(t *testing.T) {
	srcDir := t.TempDir()
	tmp, cleanup, err := snapshotStore(context.Background(), srcDir)
	if err == nil {
		cleanup()
		t.Fatal("snapshotStore succeeded for a directory with no context.db")
	}
	cleanup()
	if tmp != "" {
		t.Fatalf("error path returned a staging dir %q", tmp)
	}
	if _, err := os.Stat(filepath.Join(srcDir, "context.db")); !os.IsNotExist(err) {
		t.Fatalf("missing store was probed as empty: context.db created in srcDir, stat err=%v", err)
	}
}
