package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// TestWalkFilteredFiles_AbandonsOnContextDeadline is a regression test for a
// real, reported hang: filepath.WalkDir's own per-entry callback only
// observes ctx cancellation BETWEEN callback invocations (a non-blocking
// select), never during the blocking os.ReadDir/Lstat syscall WalkDir issues
// internally. A single stalled syscall (stale NFS/FUSE handle, a directory
// removed mid-walk) previously hung the caller - grep/glob/inspect_repository
// - forever, with no way for a turn deadline or dispatcher timeout to
// reclaim it. This test cannot force a real stuck syscall portably, so it
// proves the same escape mechanism the fix relies on: a slow visit callback
// (standing in for a slow underlying I/O op) must not hold the caller past
// ctx's deadline.
func TestWalkFilteredFiles_AbandonsOnContextDeadline(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := ws.Abs
	for i := 0; i < 5; i++ {
		if err := os.WriteFile(filepath.Join(dir, "f"+string(rune('0'+i))+".txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	blockForever := make(chan struct{})
	t.Cleanup(func() { close(blockForever) })

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	errs := &walkErrors{maxErrs: 10}
	done := make(chan error, 1)
	go func() {
		done <- walkFilteredFiles(ctx, ws, dir, "", nil, nil, ignoreView{}, false, errs, func(path, rel string, info os.FileInfo) error {
			// Stand in for a syscall that never returns: block until the
			// test itself tears down, far past ctx's deadline.
			<-blockForever
			return nil
		})
	}()

	select {
	case err := <-done:
		if err == nil || !isContextErr(err) {
			t.Fatalf("expected a context error once the deadline passed, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("walkFilteredFiles did not return within 2s of a 50ms context deadline: the caller is still hung on the blocked visit callback")
	}
}

func isContextErr(err error) bool {
	return err == context.DeadlineExceeded || err == context.Canceled
}

// TestExecuteGrep_PlainContextCanceledIsNeverExempted is a regression test
// for a data race a code review round found and confirmed with -race: grep's
// error guard used to carry `&& err != context.Canceled`, a leftover from
// when walkFilteredFiles ran synchronously. Once the walk moved to a
// background goroutine (this same change), that exemption meant a bare
// context.Canceled fell through the guard and let executeGrep read
// matches/errs while the abandoned walk goroutine could still be writing to
// them - a genuine, -race-detectable data race, not a theoretical one. This
// test pins the fix at the same layer the bug lived in: the guard itself,
// not just walkFilteredFiles.
func TestExecuteGrep_PlainContextCanceledIsNeverExempted(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.Abs, "f.txt"), []byte("match\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &grepTool{ws: ws, maxMatches: 0, maxBytes: 0}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Already canceled before the walk starts.

	_, err = tool.Execute(ctx, []byte(`{"pattern":"match"}`))
	if !isContextErr(err) {
		t.Fatalf("expected executeGrep to return the context.Canceled error unfiltered, got %v", err)
	}
}
