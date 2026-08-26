package tools

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestReadDirWithContext_AbandonsOnContextDeadline is the list_dir analogue
// of TestWalkFilteredFiles_AbandonsOnContextDeadline (walk_cancel_test.go):
// os.ReadDir is ctx-blind (the Go stdlib call accepts no context and cannot
// be interrupted), so a stalled syscall (stale NFS/FUSE handle, a degraded
// mount) previously hung list_dir's flat listing path (list_dir.go's old
// direct `os.ReadDir(abs)` call at the depth==1 branch, and walkTree's calls
// for the recursive path) forever - the dispatcher's per-call context
// timeout wrapped it from the outside but had no effect on when the syscall
// itself returned. readDirWithContextFn races the read in a goroutine
// against ctx.Done(), the same escape mechanism walkFilteredFiles and
// readFileWithContext already use elsewhere in this package.
func TestReadDirWithContext_AbandonsOnContextDeadline(t *testing.T) {
	blockForever := make(chan struct{})
	t.Cleanup(func() { close(blockForever) })

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := readDirWithContextFn(ctx, "irrelevant", func(string) ([]os.DirEntry, error) {
			// Stand in for a syscall that never returns.
			<-blockForever
			return nil, nil
		})
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil || !isContextErr(err) {
			t.Fatalf("expected a context error once the deadline passed, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("readDirWithContextFn did not return within 2s of a 50ms context deadline: the caller is still hung on the blocked read")
	}
}

// TestReadDirWithContext_AlreadyCanceledReturnsImmediately guards the
// fast-path check: a context canceled before the call even starts must not
// spin up a goroutine and wait for it - it fails closed immediately.
func TestReadDirWithContext_AlreadyCanceledReturnsImmediately(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	called := false
	_, err := readDirWithContextFn(ctx, "irrelevant", func(string) ([]os.DirEntry, error) {
		called = true
		return nil, nil
	})
	if err == nil || !isContextErr(err) {
		t.Fatalf("expected a context error for an already-canceled ctx, got %v", err)
	}
	if called {
		t.Fatal("expected the underlying read not to run at all for an already-canceled ctx")
	}
}

// TestReadDirWithContext_SuccessPassesThrough proves the happy path is
// unaffected: a normal, fast read returns its real result.
func TestReadDirWithContext_SuccessPassesThrough(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/f.txt", []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err := readDirWithContext(context.Background(), dir)
	if err != nil {
		t.Fatalf("readDirWithContext: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "f.txt" {
		t.Fatalf("entries = %+v, want [f.txt]", entries)
	}
}
