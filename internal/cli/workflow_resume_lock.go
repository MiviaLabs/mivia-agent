package cli

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const workflowExecutionLockDir = ".mivia-workflow-locks"

var (
	workflowExecutionLockOpen = openMarkerExcludeLockFile
	workflowExecutionLockStat = func(file *os.File) (os.FileInfo, error) { return file.Stat() }
	workflowExecutionLockFile = lockWorktreeMarkerFile
	workflowExecutionHooks    = installHookSession
	workflowStoreAbs          = filepath.Abs
	workflowStoreStat         = os.Stat
	workflowExecutionOpenRoot = os.OpenRoot
	workflowExecutionOpenDir  = func(root *os.Root, path string) (*os.Root, error) { return root.OpenRoot(path) }
)

func beginWorkflowExecution(workspaceRoot, storePath, runID string) (func(), error) {
	release, err := acquireWorkflowExecutionLock(storePath, runID)
	if err != nil {
		return nil, err
	}
	uninstall, err := workflowExecutionHooks(workspaceRoot, false)
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
func acquireWorkflowExecutionLockBounded(storePath, runID string, maxWait time.Duration) (func(), error) {
	deadline := time.Now().Add(maxWait)
	var lastErr error
	for {
		release, err := acquireWorkflowExecutionLock(storePath, runID)
		if err == nil {
			return release, nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("acquire workflow execution lock: %w (still held after %s; the run may still be settling - retry shortly)", lastErr, maxWait)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func acquireWorkflowExecutionLock(storePath, runID string) (func(), error) {
	lockRoot, storeIdentity, err := workflowStoreLockIdentity(storePath)
	if err != nil {
		return nil, err
	}
	root, err := workflowExecutionOpenRoot(lockRoot)
	if err != nil {
		return nil, fmt.Errorf("open workflow execution lock root: %w", err)
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
