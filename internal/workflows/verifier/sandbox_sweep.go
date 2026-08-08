package verifier

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// sandboxRootPrefix names every sandbox root this package creates, so an
// abandoned one can be recognised later.
const sandboxRootPrefix = "mivia-verifier-"

// staleSandboxAge is how long a sandbox root must be untouched before it is
// treated as abandoned. One sandbox hosts a single bounded command, so this
// is far longer than any live root survives.
const staleSandboxAge = 6 * time.Hour

// sweepStaleSandboxRoots removes sandbox roots left behind by a process that
// died without unwinding.
//
// runSandboxedCommand defers removal of its own root, which covers every
// return path including context cancellation, but a deferred call does not
// run when the process is killed: SIGKILL, a `timeout` kill, or Ctrl-C
// during a workflow run all strand the root. Nothing else ever removes it,
// and on a host where TMPDIR is a tmpfs each stranded root holds its bytes
// in RAM until an operator notices.
//
// Errors are deliberately ignored. This is opportunistic housekeeping in
// front of the real work; a root owned by another user, or racing with
// another sweep, must never fail the verification the caller asked for.
func sweepStaleSandboxRoots(dir string, now time.Time, maxAge time.Duration) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), sandboxRootPrefix) {
			continue
		}
		info, err := entry.Info()
		if err != nil || now.Sub(info.ModTime()) < maxAge {
			continue
		}
		_ = os.RemoveAll(filepath.Join(dir, entry.Name()))
	}
}

// newSandboxRoot sweeps abandoned roots, then creates this run's root.
func newSandboxRoot() (string, error) {
	sweepStaleSandboxRoots(os.TempDir(), time.Now(), staleSandboxAge)
	root, err := os.MkdirTemp("", sandboxRootPrefix)
	if err != nil {
		return "", fmt.Errorf("create verifier sandbox: %w", err)
	}
	return root, nil
}
