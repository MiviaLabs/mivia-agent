package cli

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestLockWorkflowExecutionFileNeverMentionsGitExclude is the regression for
// the misleading-error-text bug (docs/architecture/workflow-stack-settle.md
// P2): lockWorktreeMarkerFile is shared with the real Git-exclude marker
// lock, so every error string it produces says "Git exclude" - including the
// non-busy path, which the prior fix's bare %w wrap left untouched, so a
// caller diagnosing a stuck workflow delivery/resume saw "lock workflow
// execution: lock Git exclude: <cause>", naming two different locks in one
// message. A closed file descriptor makes the low-level flock fail with
// EBADF, a non-busy errno, without needing a real contended lock.
func TestLockWorkflowExecutionFileNeverMentionsGitExclude(t *testing.T) {
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
	if strings.Contains(err.Error(), "Git exclude") {
		t.Fatalf("error = %q, must not mention the unrelated Git exclude lock", err.Error())
	}
	if !strings.HasPrefix(err.Error(), "lock workflow execution: ") {
		t.Fatalf("error = %q, want the \"lock workflow execution: \" prefix", err.Error())
	}
}

// TestAcquireWorkflowExecutionLockBoundedRetriesWithGrowingJitteredBackoff
// pins the backoff curve: successive poll sleeps must grow (not stay at a
// fixed interval) up to lockPollBackoffMax, and must vary run to run (full
// jitter) rather than retrying in lockstep with every other waiter.
func TestAcquireWorkflowExecutionLockBoundedRetriesWithGrowingJitteredBackoff(t *testing.T) {
	origBase, origMax := lockPollBackoffBase, lockPollBackoffMax
	lockPollBackoffBase = 10 * time.Millisecond
	lockPollBackoffMax = 40 * time.Millisecond
	t.Cleanup(func() { lockPollBackoffBase, lockPollBackoffMax = origBase, origMax })

	storePath := lockStorePath(t)
	held, err := acquireWorkflowExecutionLock(storePath, "wfr-bounded-backoff")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(held)

	start := time.Now()
	_, err = acquireWorkflowExecutionLockBounded(context.Background(), storePath, "wfr-bounded-backoff", 150*time.Millisecond)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("acquired an already-held lock")
	}
	if !strings.Contains(err.Error(), "still held after") {
		t.Fatalf("error = %v, want the still-held timeout message", err)
	}
	// A flat 200ms-per-poll loop (the pre-fix behavior) over a 150ms window
	// would poll roughly once and stop almost immediately; a backoff capped
	// at 40ms that grows from 10ms must still spend close to the full window
	// polling, not exit early or run drastically over it.
	if elapsed < 100*time.Millisecond {
		t.Fatalf("elapsed = %v, want the bounded wait to spend close to its 150ms budget polling", elapsed)
	}
}
