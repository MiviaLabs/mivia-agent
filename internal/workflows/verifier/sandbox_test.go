package verifier

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSandboxCopiesRegularFilesAndRejectsSymlinks(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "go.mod"), []byte("module example.com/test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("go.mod", filepath.Join(source, "escape")); err != nil {
		t.Fatal(err)
	}
	_, err := copySandboxWorktree(source, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("copySandboxWorktree() error = %v", err)
	}
}

func TestSandboxRefusesSecretLikeFiles(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, ".env.production"), []byte("KEY=value"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := copySandboxWorktree(source, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "secret-like") {
		t.Fatalf("copySandboxWorktree() error = %v", err)
	}
}

func TestSandboxRefusesCredentialFiles(t *testing.T) {
	for _, name := range []string{".npmrc", ".netrc", "credentials.json"} {
		t.Run(name, func(t *testing.T) {
			source := t.TempDir()
			if err := os.WriteFile(filepath.Join(source, name), []byte("credential"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := copySandboxWorktree(source, t.TempDir()); err == nil || !strings.Contains(err.Error(), "secret-like") {
				t.Fatalf("copySandboxWorktree() error = %v", err)
			}
		})
	}
}

func TestApplyGoModuleBaselineRejectsChangedInputs(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "go.mod"), []byte("module changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := applyGoModuleBaseline(workDir, &GoModuleBaseline{GoMod: []byte("module admitted\n")})
	if err == nil || !strings.Contains(err.Error(), "changed go.mod") {
		t.Fatalf("applyGoModuleBaseline() error = %v", err)
	}
}

func TestSandboxDoesNotCopyGitWorktreeLink(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, ".git"), []byte("gitdir: /host/path"), 0o600); err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	if _, err := copySandboxWorktree(source, destination); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(destination, ".git")); !os.IsNotExist(err) {
		t.Fatalf("sandbox copy retained .git link: %v", err)
	}
}

func TestSandboxRejectsUnavailableBubblewrapBeforeCommand(t *testing.T) {
	original := sandboxBubblewrapPath
	sandboxBubblewrapPath = func() (string, error) { return "", os.ErrNotExist }
	t.Cleanup(func() { sandboxBubblewrapPath = original })
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "go.mod"), []byte("module example.com/test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := runFixedCommand(context.Background(), workDir, "go", "version")
	if err == nil || !strings.Contains(err.Error(), "bubblewrap") {
		t.Fatalf("runFixedCommand() error = %v", err)
	}
}

func TestGoProfileReportsHostFailureWithoutRepairEvidence(t *testing.T) {
	original := sandboxBubblewrapPath
	sandboxBubblewrapPath = func() (string, error) { return "", os.ErrNotExist }
	t.Cleanup(func() { sandboxBubblewrapPath = original })
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "go.mod"), []byte("module example.com/test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	baseline, err := CaptureGoModuleBaseline(workDir)
	if err != nil {
		t.Fatal(err)
	}
	result, err := newGoProfile(GoTestName, []commandSpec{{check: "go-test", program: "go", args: []string{"test", "./..."}}}, nil).Verify(context.Background(), Request{WorkDir: workDir, ModuleBaseline: baseline})
	if err != nil {
		t.Fatal(err)
	}
	if result.Repairable() || len(result.Checks) != 1 || result.Checks[0].Class != "host" || result.Checks[0].Detail == "" {
		t.Fatalf("host failure result = %#v", result)
	}
}

func TestSandboxDoesNotExposeParentEnvironment(t *testing.T) {
	if _, err := sandboxBubblewrapPath(); err != nil {
		t.Skipf("bubblewrap is unavailable: %v", err)
	}
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "go.mod"), []byte("module example.com/test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	testSource := `package test
import ("os"; "testing")
func TestSecret(t *testing.T) { if os.Getenv("MIVIA_VERIFIER_SENTINEL") != "" { t.Fatal("secret exposed") } }
`
	if err := os.WriteFile(filepath.Join(workDir, "environment_test.go"), []byte(testSource), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MIVIA_VERIFIER_SENTINEL", "must-not-reach-workflow-code")
	if err := runFixedCommand(context.Background(), workDir, "go", "test", "./..."); err != nil {
		t.Fatalf("sandboxed Go test failed: %v", err)
	}
}

func TestSandboxDisablesGoWorkspaceMode(t *testing.T) {
	args := sandboxArgs("/tmp/work", "/tmp/modules", "go", "test", "./...")
	joined := strings.Join(args, "\x00")
	if !strings.Contains(joined, "GOWORK\x00off") {
		t.Fatalf("sandbox arguments do not disable Go workspace mode: %q", joined)
	}
}
