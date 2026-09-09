package localengine

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/vcs"
)

// TestEnsureWorktreeTakesTheLifecycleLock pins the fix for a real gap: the
// worktree lifecycle lock had five callers and every one lived in the
// interactive CLI package. The workflow engine reached git through the
// NIL-LEASE create/remove variants under nothing but a package-level
// sync.Mutex, which is in-process only - so `mivia worktree remove <n>`
// (locked and leased) and a concurrent workflow admission on the same name had
// no mutual exclusion whatsoever.
func TestEnsureWorktreeTakesTheLifecycleLock(t *testing.T) {
	var gotRoot, gotName string
	var closed bool
	orig := lockWorktreeLifecycle
	lockWorktreeLifecycle = func(root, name string) (*vcs.WorktreeLifecycleLock, error) {
		gotRoot, gotName = root, name
		return nil, errStubLock{}
	}
	t.Cleanup(func() { lockWorktreeLifecycle = orig })

	unlock, ok := lockWorktreeName("/repo", "workflow-wfr-1")
	if ok {
		t.Fatal("a failing lock must report not-taken rather than a usable unlock")
	}
	if unlock != nil {
		t.Fatal("a failing lock must return no unlock function")
	}
	if gotRoot != "/repo" || gotName != "workflow-wfr-1" {
		t.Fatalf("lock taken for (%q, %q), want (\"/repo\", \"workflow-wfr-1\")", gotRoot, gotName)
	}
	_ = closed
}

// TestLockWorktreeNameSkipsBlankIdentity keeps the lock off a half-built
// identity, where the name would not address a real worktree.
func TestLockWorktreeNameSkipsBlankIdentity(t *testing.T) {
	called := false
	orig := lockWorktreeLifecycle
	lockWorktreeLifecycle = func(root, name string) (*vcs.WorktreeLifecycleLock, error) {
		called = true
		return nil, errStubLock{}
	}
	t.Cleanup(func() { lockWorktreeLifecycle = orig })

	for _, tc := range [][2]string{{"", "n"}, {"/repo", ""}, {"", ""}} {
		if _, ok := lockWorktreeName(tc[0], tc[1]); ok {
			t.Errorf("lockWorktreeName(%q, %q) reported taken", tc[0], tc[1])
		}
	}
	if called {
		t.Error("a blank root or name must not reach the lock at all")
	}
}

type errStubLock struct{}

func (errStubLock) Error() string { return "no lock available" }
