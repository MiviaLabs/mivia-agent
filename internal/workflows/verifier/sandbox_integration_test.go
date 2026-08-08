//go:build integration

package verifier

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSandboxRunsRepositoryProfile(t *testing.T) {
	if _, err := sandboxBubblewrapPath(); err != nil {
		t.Fatalf("bubblewrap is unavailable: %v", err)
	}
	root := sandboxRepositoryRoot(t)
	baseline, err := CaptureGoModuleBaseline(root)
	if err != nil {
		t.Fatal(err)
	}
	err = runSandboxedCommand(context.Background(), root, baseline, secretPolicy(t), "go", "test", "./...")
	if err == nil {
		return
	}
	var failure *commandFailure
	if errors.As(err, &failure) {
		t.Fatalf("sandbox error = %v; detail = %q", err, failure.detail)
	}
	t.Fatal(err)
}

// TestSandboxStructureGateCatchesUntrackedViolation pins the in-loop
// preflight_structure gate: a new untracked test file with a 123-line function
// (hard max 120) must fail the --worktree gate with source-class evidence
// naming the HARD detail, while the --all control passes blindly because the
// sandbox git index is empty (git init, nothing staged) and git ls-files
// reports zero files.
func TestSandboxStructureGateCatchesUntrackedViolation(t *testing.T) {
	if _, err := sandboxBubblewrapPath(); err != nil {
		t.Skipf("bubblewrap is unavailable: %v", err)
	}
	source := sandboxRepositoryRoot(t)
	workRoot := t.TempDir()
	if _, err := copySandboxWorktree(source, workRoot, secretPolicy(t)); err != nil {
		t.Fatal(err)
	}
	fileDir := filepath.Join(workRoot, "internal", "demo")
	if err := os.MkdirAll(fileDir, 0o700); err != nil {
		t.Fatal(err)
	}
	lines := []string{"package demo", "", "func TestCloseRunAtomicity() {"}
	for i := 0; i < 121; i++ {
		lines = append(lines, fmt.Sprintf("\t_ = %d", i))
	}
	lines = append(lines, "}", "")
	violation := filepath.Join(fileDir, "close_run_atomicity_test.go")
	if err := os.WriteFile(violation, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	baseline, err := CaptureGoModuleBaseline(workRoot)
	if err != nil {
		t.Fatal(err)
	}
	// Mirror initializeSandboxGit: an empty index, nothing staged.
	git, err := sandboxGitPath()
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.CommandContext(context.Background(), git, "init", "-q", workRoot).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}

	ctx := context.Background()
	err = runSandboxedCommand(ctx, workRoot, baseline, secretPolicy(t), "python3", "scripts/check_go_structure.py", "--strict", "--worktree")
	var failure *commandFailure
	if !errors.As(err, &failure) {
		t.Fatalf("--worktree gate error = %v; want a source commandFailure", err)
	}
	if failure.class != "source" || !strings.Contains(failure.detail, "HARD function LOC") {
		t.Fatalf("--worktree gate failure = class %q detail %q", failure.class, failure.detail)
	}

	// Control: --all reads git ls-files. The sandbox index is empty, so the
	// untracked violation is invisible and the gate passes (the blindness the
	// --worktree mode replaces).
	if err := runSandboxedCommand(ctx, workRoot, baseline, secretPolicy(t), "python3", "scripts/check_go_structure.py", "--strict", "--all"); err != nil {
		t.Fatalf("--all control gate error = %v; want nil (blind pass)", err)
	}
}
