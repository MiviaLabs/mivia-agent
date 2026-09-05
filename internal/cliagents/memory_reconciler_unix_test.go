//go:build unix

package cliagents

import (
	"context"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/memory"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// TestStartMemoryIndexReconcilerWatcherCreateFailure pins both start()'s own
// error branch (fsnotify.NewWatcher failing) and StartMemoryIndexReconciler's
// propagation of that failure. With the process's file-descriptor limit
// dropped below what inotify needs, fsnotify.NewWatcher deterministically
// fails with EMFILE ("too many open files"), and StartMemoryIndexReconciler
// must report ok=false with a nil stop func instead of returning a
// reconciler built around a watcher that never opened.
func TestStartMemoryIndexReconcilerWatcherCreateFailure(t *testing.T) {
	root := t.TempDir()
	source, err := memory.NewMarkdownSource(root, filepath.Join(t.TempDir(), "org-memories"), "")
	if err != nil {
		t.Fatal(err)
	}
	index, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = index.Close() })
	inner, err := OpenMarkdownStore(context.Background(), MarkdownStoreConfig{Source: source, Index: index, ProjectID: root})
	if err != nil {
		t.Fatal(err)
	}

	var old syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &old); err != nil {
		t.Skipf("RLIMIT_NOFILE unavailable on this platform: %v", err)
	}
	low := syscall.Rlimit{Cur: 3, Max: old.Max}
	if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &low); err != nil {
		t.Skipf("cannot lower RLIMIT_NOFILE in this sandbox: %v", err)
	}
	// The lowered limit is process-wide, so the window it is held open is
	// kept to exactly the call under test; the original limit is restored
	// immediately afterward regardless of outcome.
	stop, ok := StartMemoryIndexReconciler(inner, 50*time.Millisecond)
	if restoreErr := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &old); restoreErr != nil {
		t.Fatalf("restore RLIMIT_NOFILE: %v", restoreErr)
	}
	if ok {
		if stop != nil {
			stop()
		}
		t.Fatal("StartMemoryIndexReconciler must refuse a store when the watcher fails to open")
	}
	if stop != nil {
		t.Fatal("StartMemoryIndexReconciler must return a nil stop func on failure")
	}
}
