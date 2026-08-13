package cli

import (
	"encoding/json"
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
	release, err := acquireWorkflowExecutionLockBounded(lockStorePath(t), "wfr-free", 2*time.Second)
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

	_, err = acquireWorkflowExecutionLockBounded(storePath, "wfr-held", 250*time.Millisecond)
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

	finish, err := beginWorkflowExecutionBounded(root, storePath, "wfr-bounded-begin", 250*time.Millisecond)
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

	finish, err = beginWorkflowExecutionBounded(root, storePath, "wfr-bounded-begin", 2*time.Second)
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
