package verifier

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/secretpath"
)

func TestSandboxCopiesRegularFilesAndRejectsSymlinks(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "go.mod"), []byte("module example.com/test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("go.mod", filepath.Join(source, "escape")); err != nil {
		t.Fatal(err)
	}
	_, err := copySandboxWorktree(source, t.TempDir(), secretPolicy(t))
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("copySandboxWorktree() error = %v", err)
	}
}

func TestSandboxPreservesExecutableFiles(t *testing.T) {
	source := t.TempDir()
	path := filepath.Join(source, "hook.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	if _, err := copySandboxWorktree(source, destination, secretPolicy(t)); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(destination, "hook.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("sandbox copied non-executable file mode %v", info.Mode())
	}
}

func TestSandboxOmitsSecretLikeFiles(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, ".env.production"), []byte("KEY=value"), 0o600); err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	if _, err := copySandboxWorktree(source, destination, secretPolicy(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(destination, ".env.production")); !os.IsNotExist(err) {
		t.Fatalf("sandbox retained secret file: %v", err)
	}
}

func TestSandboxOmitsCredentialFiles(t *testing.T) {
	for _, name := range []string{".npmrc", ".netrc", "credentials.json"} {
		t.Run(name, func(t *testing.T) {
			source := t.TempDir()
			if err := os.WriteFile(filepath.Join(source, name), []byte("credential"), 0o600); err != nil {
				t.Fatal(err)
			}
			destination := t.TempDir()
			if _, err := copySandboxWorktree(source, destination, secretPolicy(t)); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(destination, name)); !os.IsNotExist(err) {
				t.Fatalf("sandbox retained secret file: %v", err)
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
	if _, err := copySandboxWorktree(source, destination, secretPolicy(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(destination, ".git")); !os.IsNotExist(err) {
		t.Fatalf("sandbox copy retained .git link: %v", err)
	}
}

func TestSandboxDoesNotCopyNestedGitWorktreeLink(t *testing.T) {
	source := t.TempDir()
	nested := filepath.Join(source, ".mivia", "worktrees", "child")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, ".git"), []byte("gitdir: /host/path"), 0o600); err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	if _, err := copySandboxWorktree(source, destination, secretPolicy(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(destination, ".mivia", "worktrees", "child", ".git")); !os.IsNotExist(err) {
		t.Fatalf("sandbox copied nested git link: %v", err)
	}
}

func TestSandboxDoesNotCopyManagedWorktrees(t *testing.T) {
	source := t.TempDir()
	path := filepath.Join(source, ".mivia", "worktrees", "child", "source.go")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("package child\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	if _, err := copySandboxWorktree(source, destination, secretPolicy(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(destination, ".mivia", "worktrees")); !os.IsNotExist(err) {
		t.Fatalf("sandbox copied managed worktrees: %v", err)
	}
}

func TestSandboxDoesNotCopyCodeGraphCache(t *testing.T) {
	source := t.TempDir()
	path := filepath.Join(source, ".codegraph", "cache.db")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("cache"), 0o600); err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	if _, err := copySandboxWorktree(source, destination, secretPolicy(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(destination, ".codegraph")); !os.IsNotExist(err) {
		t.Fatalf("sandbox copied CodeGraph cache: %v", err)
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

func TestVerifierGoToolchainRejectsHostHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := rejectHostHomePath(home); err == nil || !strings.Contains(err.Error(), "host home") {
		t.Fatalf("rejectHostHomePath() error = %v, want host home error", err)
	}
}

func TestSandboxModuleCopyAllowsNonCredentialTokenNames(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "token.go"), []byte("package token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := copySandboxTree(source, t.TempDir(), secretpath.Policy{}); err != nil {
		t.Fatalf("copySandboxTree() error = %v", err)
	}
}

func secretPolicy(t *testing.T) secretpath.Policy {
	t.Helper()
	policy, err := secretpath.New([]string{".env", ".pem", ".key", "id_rsa", "id_ed25519", ".npmrc", ".netrc", "credentials"}, []string{".env.example"})
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func sandboxRepositoryRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repository go.mod is missing")
		}
		dir = parent
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

func TestSandboxDisablesGoWorkspaceMode(t *testing.T) {
	args := sandboxArgs("/tmp/work", "/tmp/modules", "/tmp/home", "/opt/go", "/opt/go/bin/go", true, "test", "./...")
	joined := strings.Join(args, "\x00")
	if !strings.Contains(joined, "GOWORK\x00off") {
		t.Fatalf("sandbox arguments do not disable Go workspace mode: %q", joined)
	}
	if !strings.Contains(joined, "--tmpfs\x00/home\x00--bind\x00/tmp/home\x00/home/sandbox") {
		t.Fatalf("sandbox arguments do not create the isolated home parent: %q", joined)
	}
	if !strings.Contains(joined, "--ro-bind\x00/opt/go\x00/opt/go") || !strings.HasSuffix(joined, "--\x00/opt/go/bin/go\x00test\x00./...") {
		t.Fatalf("sandbox arguments do not mount and run the Go toolchain: %q", joined)
	}
}

func TestSandboxedCommandClassifiesStdoutBwrapPrefixAsSource(t *testing.T) {
	stubBubblewrapPath(t, writeFakeBwrapPassthrough(t))
	stubGitPath(t, writeFakeGit(t))

	workDir := t.TempDir()
	writeGoMod(t, workDir)
	mainPath := filepath.Join(workDir, "main.go")
	program := `package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Println("bwrap: this is workspace output, not a host failure")
	os.Exit(1)
}
`
	if err := os.WriteFile(mainPath, []byte(program), 0o600); err != nil {
		t.Fatal(err)
	}

	baseline, err := CaptureGoModuleBaseline(workDir)
	if err != nil {
		t.Fatal(err)
	}
	profile := newGoProfile("bwrap-stdout-test", []commandSpec{{check: "bwrap-stdout", program: "go", args: []string{"run", mainPath}}}, nil, secretpath.Policy{})
	result, err := profile.Verify(context.Background(), Request{WorkDir: workDir, ModuleBaseline: baseline})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Checks) != 1 {
		t.Fatalf("expected 1 check, got %#v", result.Checks)
	}
	check := result.Checks[0]
	if check.Class != "source" {
		t.Fatalf("expected source class, got class=%q detail=%q", check.Class, check.Detail)
	}
	if !result.Repairable() {
		t.Fatalf("expected repairable source result, got %#v", result)
	}
}

func TestSandboxedCommandClassifiesStderrBwrapPrefixAsHost(t *testing.T) {
	stubBubblewrapPath(t, writeFakeBwrapUnavailable(t))
	stubGitPath(t, writeFakeGit(t))

	workDir := t.TempDir()
	writeGoMod(t, workDir)
	baseline, err := CaptureGoModuleBaseline(workDir)
	if err != nil {
		t.Fatal(err)
	}
	profile := newGoProfile("bwrap-stderr-test", []commandSpec{{check: "bwrap-stderr", program: "go", args: []string{"version"}}}, nil, secretpath.Policy{})
	result, err := profile.Verify(context.Background(), Request{WorkDir: workDir, ModuleBaseline: baseline})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Checks) != 1 {
		t.Fatalf("expected 1 check, got %#v", result.Checks)
	}
	check := result.Checks[0]
	if check.Class != "host" {
		t.Fatalf("expected host class, got class=%q detail=%q", check.Class, check.Detail)
	}
	if result.Repairable() {
		t.Fatalf("expected non-repairable host result, got %#v", result)
	}
}

func TestSandboxedCommandClassifiesMissingBwrapAsHost(t *testing.T) {
	stubBubblewrapPath(t, filepath.Join(t.TempDir(), "missing-bwrap"))
	stubGitPath(t, writeFakeGit(t))

	workDir := t.TempDir()
	writeGoMod(t, workDir)
	baseline, err := CaptureGoModuleBaseline(workDir)
	if err != nil {
		t.Fatal(err)
	}
	profile := newGoProfile("missing-bwrap-test", []commandSpec{{check: "missing-bwrap", program: "go", args: []string{"version"}}}, nil, secretpath.Policy{})
	result, err := profile.Verify(context.Background(), Request{WorkDir: workDir, ModuleBaseline: baseline})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Checks) != 1 {
		t.Fatalf("expected 1 check, got %#v", result.Checks)
	}
	check := result.Checks[0]
	if check.Class != "host" {
		t.Fatalf("expected host class, got class=%q detail=%q", check.Class, check.Detail)
	}
	if result.Repairable() {
		t.Fatalf("expected non-repairable host result, got %#v", result)
	}
}

func writeGoMod(t *testing.T, workDir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(workDir, "go.mod"), []byte("module example.com/test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeFakeBwrapPassthrough(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "bwrap")
	script := "#!/bin/sh\nwhile [ \"$1\" != \"--\" ]; do\n    shift\ndone\nshift\nexec \"$@\"\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeFakeBwrapUnavailable(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "bwrap")
	script := "#!/bin/sh\necho \"bwrap: command not found\" >&2\nexit 1\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeFakeGit(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "git")
	script := "#!/bin/sh\nexit 0\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func stubBubblewrapPath(t *testing.T, path string) {
	t.Helper()
	original := sandboxBubblewrapPath
	sandboxBubblewrapPath = func() (string, error) { return path, nil }
	t.Cleanup(func() { sandboxBubblewrapPath = original })
}

func stubGitPath(t *testing.T, path string) {
	t.Helper()
	original := sandboxGitPath
	sandboxGitPath = func() (string, error) { return path, nil }
	t.Cleanup(func() { sandboxGitPath = original })
}
