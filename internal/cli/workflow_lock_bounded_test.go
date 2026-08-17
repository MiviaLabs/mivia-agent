package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestInputsToRawFlagsPreservesBigIntegers pins that json.Number values from
// the UseNumber tool decode reach the admission flags verbatim.
func TestInputsToRawFlagsPreservesBigIntegers(t *testing.T) {
	flags, err := inputsToRawFlags(map[string]any{"n": json.Number("9007199254740993")})
	if err != nil {
		t.Fatal(err)
	}
	if len(flags) != 1 || flags[0] != "n=9007199254740993" {
		t.Fatalf("flags = %v, want n=9007199254740993", flags)
	}
}

func lockStorePath(t *testing.T) string {
	t.Helper()
	store := t.TempDir()
	storePath := filepath.Join(store, "context.db")
	if err := os.WriteFile(storePath, []byte("store"), 0o600); err != nil {
		t.Fatal(err)
	}
	return storePath
}

// TestAcquireWorkflowExecutionLockBoundedSucceedsWhenFree pins the bounded
// variant still acquires immediately when the lock is free.
func TestAcquireWorkflowExecutionLockBoundedSucceedsWhenFree(t *testing.T) {
	release, err := acquireWorkflowExecutionLockBounded(context.Background(), lockStorePath(t), "wfr-free", 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	release()
}

// TestAcquireWorkflowExecutionLockBoundedTimesOutWithClearError pins that a
// held lock surfaces as an explained, retry-able error instead of the opaque
// "lock is busy" failure while the run keeps running.
func TestAcquireWorkflowExecutionLockBoundedTimesOutWithClearError(t *testing.T) {
	storePath := lockStorePath(t)
	hold, err := acquireWorkflowExecutionLock(storePath, "wfr-held")
	if err != nil {
		t.Fatal(err)
	}
	defer hold()

	_, err = acquireWorkflowExecutionLockBounded(context.Background(), storePath, "wfr-held", 250*time.Millisecond)
	if err == nil {
		t.Fatal("expected a busy-lock error")
	}
	if !strings.Contains(err.Error(), "still held") {
		t.Fatalf("error %q should explain the held lock and suggest a retry", err.Error())
	}
}

// TestBeginWorkflowExecutionBoundedWaitsForHeldLock pins
// beginWorkflowExecutionBounded (the cancel/deliver admission path) waits,
// bounded, for a concurrent holder to release the execution flock instead of
// failing with the plain lock's opaque "lock is busy" error, then succeeds
// once the holder releases, and that its release func returns the flock to a
// plain acquire.
func TestBeginWorkflowExecutionBoundedWaitsForHeldLock(t *testing.T) {
	storePath := lockStorePath(t)
	originalHooks := workflowExecutionHooks
	workflowExecutionHooks = func(string, bool, bool) (func(), error) { return func() {}, nil }
	t.Cleanup(func() { workflowExecutionHooks = originalHooks })
	root := t.TempDir()

	hold, err := acquireWorkflowExecutionLock(storePath, "wfr-bounded-begin")
	if err != nil {
		t.Fatal(err)
	}

	finish, err := beginWorkflowExecutionBounded(context.Background(), root, storePath, "wfr-bounded-begin", 250*time.Millisecond)
	if err == nil {
		if finish != nil {
			finish()
		}
		t.Fatal("expected a busy-lock error while the lock is held")
	}
	if !strings.Contains(err.Error(), "still held after") {
		t.Fatalf("error %q should explain the held lock and suggest a retry", err.Error())
	}

	hold()

	finish, err = beginWorkflowExecutionBounded(context.Background(), root, storePath, "wfr-bounded-begin", 2*time.Second)
	if err != nil {
		t.Fatalf("begin after release: %v", err)
	}

	// The returned release func must return the flock to a plain acquire.
	finish()
	again, err := acquireWorkflowExecutionLock(storePath, "wfr-bounded-begin")
	if err != nil {
		t.Fatalf("plain acquire after begin's release = %v, want success", err)
	}
	again()
}

// TestLockWorkflowExecutionFileWrapsNonBusyError pins that
// lockWorkflowExecutionFile forwards a non-busy failure from the underlying
// marker-lock primitive under its own "lock workflow execution" wording
// (workflow_resume_lock.go:36), not the busy-specific rewrite reserved for
// "lock is busy" (line 34). A closed file descriptor makes the low-level
// flock fail with EBADF, a non-busy errno, without needing a real contended
// lock.
func TestLockWorkflowExecutionFileWrapsNonBusyError(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "marker-lock")
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = lockWorkflowExecutionFile(file)
	if err == nil {
		t.Fatal("closed marker descriptor acquired a workflow execution lock")
	}
	if !strings.HasPrefix(err.Error(), "lock workflow execution: ") {
		t.Fatalf("error = %q, want the generic \"lock workflow execution: %%w\" wrap", err.Error())
	}
	if strings.Contains(err.Error(), "lock is busy") {
		t.Fatalf("error = %q, want a non-busy error routed through the generic wrap, not the busy rewrite", err.Error())
	}
}

// TestAcquireWorkflowExecutionLockBoundedRetryLoopRespectsDeadline pins the
// retry-sleep loop's deadline-clamped sleep (workflow_resume_lock.go:96-100).
// A single contended attempt against an already-held lock costs about 1.03s
// (the underlying flock primitive retries internally for up to 100*10ms
// before reporting busy - see lockWorktreeMarkerFile), and the deadline is
// only rechecked BETWEEN attempts, never during one. With maxWait set just
// past one attempt's cost, the loop's first attempt leaves under 200ms
// remaining, forcing the clamp at line 99 (sleep = remaining) rather than the
// unclamped 200ms tick; the loop then starts a second, uncancellable attempt
// before the deadline check fires again. This pins the known overshoot shape
// (roughly one extra attempt's worth of time, not an unbounded hang) with a
// real timing assertion rather than just checking the error text.
func TestAcquireWorkflowExecutionLockBoundedRetryLoopRespectsDeadline(t *testing.T) {
	storePath := lockStorePath(t)
	hold, err := acquireWorkflowExecutionLock(storePath, "wfr-deadline")
	if err != nil {
		t.Fatal(err)
	}
	defer hold()

	const maxWait = 1150 * time.Millisecond
	start := time.Now()
	_, err = acquireWorkflowExecutionLockBounded(context.Background(), storePath, "wfr-deadline", maxWait)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected a busy-lock error")
	}
	if !strings.Contains(err.Error(), "still held after") {
		t.Fatalf("error %q should explain the held lock and suggest a retry", err.Error())
	}
	if elapsed < maxWait {
		t.Fatalf("elapsed = %s, want at least maxWait %s", elapsed, maxWait)
	}
	// The deadline is only rechecked between attempts, so overshoot is bounded
	// by one extra ~1.03s attempt plus scheduling slack, not unbounded.
	const maxOvershoot = 1600 * time.Millisecond
	if overshoot := elapsed - maxWait; overshoot > maxOvershoot {
		t.Fatalf("elapsed = %s (maxWait %s, overshoot %s); want overshoot bounded by roughly one retry attempt (%s)", elapsed, maxWait, overshoot, maxOvershoot)
	}
}

// TestAcquireWorkflowExecutionLockBoundedCtxCancelDuringSleep pins that the
// retry loop's select (workflow_resume_lock.go:101-104) reacts to context
// cancellation that arrives mid-sleep, not just a context already cancelled
// before the loop starts (TestBeginWorkflowExecutionBoundedHonorsContext
// covers that earlier short-circuit at the top of the loop). The cancel fires
// during the outer 200ms sleep tick that follows the first (uncancellable,
// ~1.03s) contended attempt, so a prompt return proves the select's
// ctx.Done() case actually won the race against time.After(sleep), not that
// the loop simply never got that far.
func TestAcquireWorkflowExecutionLockBoundedCtxCancelDuringSleep(t *testing.T) {
	storePath := lockStorePath(t)
	hold, err := acquireWorkflowExecutionLock(storePath, "wfr-ctx-mid-sleep")
	if err != nil {
		t.Fatal(err)
	}
	defer hold()

	ctx, cancel := context.WithCancel(context.Background())
	const cancelAfter = 1150 * time.Millisecond
	timer := time.AfterFunc(cancelAfter, cancel)
	defer timer.Stop()

	start := time.Now()
	_, err = acquireWorkflowExecutionLockBounded(ctx, storePath, "wfr-ctx-mid-sleep", 5*time.Second)
	elapsed := time.Since(start)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("bounded acquire with mid-sleep cancel = %v, want context.Canceled", err)
	}
	// A second contended attempt alone costs ~1.03s; returning well under that
	// after cancelAfter proves the select reacted to ctx.Done() mid-sleep
	// instead of finishing the sleep tick and starting another attempt.
	if elapsed >= cancelAfter+800*time.Millisecond {
		t.Fatalf("elapsed = %s, want prompt return near cancelAfter %s, not a further contended attempt or the full 5s maxWait", elapsed, cancelAfter)
	}
}

// TestBeginWorkflowExecutionBoundedHonorsContext pins that the bounded begin
// path returns immediately on a cancelled context instead of sleeping through
// the full maxWait.
func TestBeginWorkflowExecutionBoundedHonorsContext(t *testing.T) {
	storePath := lockStorePath(t)
	originalHooks := workflowExecutionHooks
	workflowExecutionHooks = func(string, bool, bool) (func(), error) { return func() {}, nil }
	t.Cleanup(func() { workflowExecutionHooks = originalHooks })
	root := t.TempDir()

	hold, err := acquireWorkflowExecutionLock(storePath, "wfr-bounded-ctx")
	if err != nil {
		t.Fatal(err)
	}
	defer hold()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	_, err = beginWorkflowExecutionBounded(ctx, root, storePath, "wfr-bounded-ctx", 5*time.Second)
	elapsed := time.Since(start)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("begin with cancelled context = %v, want context.Canceled", err)
	}
	if elapsed >= 500*time.Millisecond {
		t.Fatalf("begin with cancelled context took %s; want well under maxWait", elapsed)
	}
}
