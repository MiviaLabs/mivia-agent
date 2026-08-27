package cliworkflow

import (
	"context"
	"crypto/sha256"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/cliworktree"
)

const workflowExecutionLockDir = ".mivia-workflow-locks"

var (
	workflowExecutionLockOpen = cliworktree.OpenMarkerExcludeLockFile
	workflowExecutionLockStat = func(file *os.File) (os.FileInfo, error) { return file.Stat() }
	workflowExecutionLockFile = LockWorkflowExecutionFile
	workflowStoreAbs          = filepath.Abs
	workflowStoreStat         = os.Stat
	workflowExecutionOpenRoot = os.OpenRoot
	workflowExecutionOpenDir  = func(root *os.Root, path string) (*os.Root, error) { return root.OpenRoot(path) }
)

// LockWorkflowExecutionFile wraps the low-level file-lock primitive so the
// workflow execution lock reports its own name instead of borrowing the Git
// exclude lock's message. The primitive is not changed; the wording is fixed
// at this workflow-specific call site. lockWorktreeMarkerFile is shared with
// the actual Git-exclude marker lock, so every error string it can produce
// says "Git exclude" - not just the busy case a prior fix special-cased. That
// left every other failure (permission denied, I/O error, ...) reporting the
// wrong lock's name to a caller trying to diagnose a stuck workflow delivery
// or resume. renameGitExcludeLockError rewrites the whole family generically.
func LockWorkflowExecutionFile(file *os.File) (func(), error) {
	unlock, err := cliworktree.LockWorktreeMarkerFile(file)
	if err != nil {
		return nil, renameGitExcludeLockError(err)
	}
	return unlock, nil
}

// gitExcludeLockPrefix is the exact prefix lockWorktreeMarkerFile uses for
// every error it returns (both the wrapped-cause and the bare "lock is busy"
// shapes, on every platform). renameGitExcludeLockError swaps it for
// workflow-execution wording; a message that does not carry this prefix (not
// possible from the current primitive, but checked so this stays a rename,
// not a lossy rewrite) is wrapped instead of mangled.
const gitExcludeLockPrefix = "lock Git exclude:"

func renameGitExcludeLockError(err error) error {
	msg := err.Error()
	if rest, ok := strings.CutPrefix(msg, gitExcludeLockPrefix); ok {
		return fmt.Errorf("lock workflow execution:%s", rest)
	}
	return fmt.Errorf("lock workflow execution: %w", err)
}

func BeginWorkflowExecution(workspaceRoot, storePath, runID string) (func(), error) {
	release, err := AcquireWorkflowExecutionLock(storePath, runID)
	if err != nil {
		return nil, err
	}
	uninstall, err := WorkflowExecutionHooks(workspaceRoot, false, false)
	if err != nil {
		release()
		return nil, err
	}
	return func() {
		uninstall()
		release()
	}, nil
}

// BeginWorkflowExecutionBounded is BeginWorkflowExecution with a bounded wait
// for a concurrent holder (a settling controller) to release the execution
// flock: cancel and deliver use it because the plain lock fails with an
// opaque "lock is busy" error while the in-process controller is still
// releasing the flock after the cancel wait bound.
func BeginWorkflowExecutionBounded(ctx context.Context, workspaceRoot, storePath, runID string, maxWait time.Duration) (func(), error) {
	release, err := acquireWorkflowExecutionLockBounded(ctx, storePath, runID, maxWait)
	if err != nil {
		return nil, err
	}
	uninstall, err := WorkflowExecutionHooks(workspaceRoot, false, false)
	if err != nil {
		release()
		return nil, err
	}
	return func() {
		uninstall()
		release()
	}, nil
}

// acquireWorkflowExecutionLockBounded acquires the execution lock with a
// bounded wait for a concurrent holder (a settling controller) to release it.
// Cancel and deliver use it because the non-blocking admission lock fails
// with an opaque "lock is busy" error while the in-process controller is
// still releasing the flock after the cancel wait bound.
// lockPollBackoffBase and lockPollBackoffMax bound the exponential backoff
// between poll attempts: starting small keeps a short-lived contention (a
// deliver mid-git-push) resolving fast, capping at 5s keeps a long wait
// (WorkflowResolutionLockWait=60s) from polling so slowly that the holder's
// release goes unnoticed for seconds after the fact. Vars, not consts, so a
// test can shrink both without waiting out the real backoff curve.
var (
	lockPollBackoffBase = 200 * time.Millisecond
	lockPollBackoffMax  = 5 * time.Second
)

// lockAttemptResult carries a contended AcquireWorkflowExecutionLock
// outcome from the attempt goroutine in acquireWorkflowExecutionLockBounded
// back to its caller (or to the abandon-cleanup goroutine, if the caller
// already gave up on a context cancellation).
type lockAttemptResult struct {
	release func()
	err     error
}

func acquireWorkflowExecutionLockBounded(ctx context.Context, storePath, runID string, maxWait time.Duration) (func(), error) {
	deadline := time.Now().Add(maxWait)
	backoff := lockPollBackoffBase
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// AcquireWorkflowExecutionLock's contended path blocks for up to ~1s
		// inside lockWorktreeMarkerFile's flock retry loop, which is not
		// context-aware. Running it in a goroutine and racing it against
		// ctx.Done() keeps a mid-attempt cancellation prompt instead of
		// silently waiting out that ~1s. If ctx wins the race, the attempt
		// goroutine is abandoned but not leaked: the cleanup goroutine below
		// releases the lock immediately if the abandoned attempt later
		// succeeds, so a cancelled caller never leaves the lock held.
		attempt := make(chan lockAttemptResult, 1)
		go func() {
			release, err := AcquireWorkflowExecutionLock(storePath, runID)
			attempt <- lockAttemptResult{release: release, err: err}
		}()
		var release func()
		var err error
		select {
		case res := <-attempt:
			release, err = res.release, res.err
		case <-ctx.Done():
			go func() {
				if res := <-attempt; res.err == nil {
					res.release()
				}
			}()
			return nil, ctx.Err()
		}
		if err == nil {
			return release, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%w (still held after %s; the run may still be settling - retry shortly)", err, maxWait)
		}
		// Exponential backoff with full jitter (0..backoff): a fixed poll
		// interval means every waiter retries in lockstep, so a lock that
		// frees for one poll window can be lost to another waiter that polled
		// a moment earlier - jitter spreads retries out instead.
		sleep := time.Duration(rand.Int63n(int64(backoff)) + 1)
		if remaining := time.Until(deadline); remaining < sleep {
			sleep = remaining
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(sleep):
		}
		if backoff < lockPollBackoffMax {
			backoff *= 2
			if backoff > lockPollBackoffMax {
				backoff = lockPollBackoffMax
			}
		}
	}
}

func AcquireWorkflowExecutionLock(storePath, runID string) (func(), error) {
	lockRoot, storeIdentity, err := workflowStoreLockIdentity(storePath)
	if err != nil {
		return nil, err
	}
	root, err := workflowExecutionOpenRoot(lockRoot)
	if err != nil {
		return nil, fmt.Errorf("open workflow execution lock Root: %w", err)
	}
	closeRoot := true
	defer func() {
		if closeRoot {
			_ = root.Close()
		}
	}()
	if err := root.MkdirAll(workflowExecutionLockDir, 0o700); err != nil {
		return nil, fmt.Errorf("create workflow execution lock directory: %w", err)
	}
	dir, err := workflowExecutionOpenDir(root, workflowExecutionLockDir)
	if err != nil {
		return nil, fmt.Errorf("open workflow execution lock directory: %w", err)
	}
	closeLockRoot := true
	defer func() {
		if closeLockRoot {
			_ = dir.Close()
		}
	}()
	name := fmt.Sprintf("workflow-%x.lock", sha256.Sum256([]byte(storeIdentity+"\x00"+runID)))
	file, err := workflowExecutionLockOpen(dir, name)
	if err != nil {
		return nil, fmt.Errorf("open workflow execution lock: %w", err)
	}
	info, err := workflowExecutionLockStat(file)
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		if err != nil {
			return nil, fmt.Errorf("inspect workflow execution lock: %w", err)
		}
		return nil, fmt.Errorf("workflow execution lock is not a regular file")
	}
	unlock, err := workflowExecutionLockFile(file)
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("acquire workflow execution lock: %w", err)
	}
	closeRoot = false
	closeLockRoot = false
	return func() {
		unlock()
		_ = file.Close()
		_ = dir.Close()
		_ = root.Close()
	}, nil
}

func workflowStoreLockIdentity(storePath string) (string, string, error) {
	abs, err := workflowStoreAbs(storePath)
	if err != nil {
		return "", "", fmt.Errorf("resolve workflow store path: %w", err)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(abs); resolveErr == nil {
		abs = resolved
		info, statErr := workflowStoreStat(abs)
		if statErr != nil {
			return "", "", fmt.Errorf("inspect workflow store path: %w", statErr)
		}
		if !workflowStoreHasSingleLink(abs, info) {
			return "", "", fmt.Errorf("workflow store must not have hard links")
		}
	} else if !os.IsNotExist(resolveErr) {
		return "", "", fmt.Errorf("resolve workflow store path: %w", resolveErr)
	} else {
		parent, parentErr := filepath.EvalSymlinks(filepath.Dir(abs))
		if parentErr != nil {
			return "", "", fmt.Errorf("resolve workflow store directory: %w", parentErr)
		}
		abs = filepath.Join(parent, filepath.Base(abs))
	}
	return filepath.Dir(abs), abs, nil
}
