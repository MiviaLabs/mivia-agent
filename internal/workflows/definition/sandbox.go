package definition

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/redact"
	"github.com/MiviaLabs/mivia-agent/internal/secretpath"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

const maxVerifierDiagnosticBytes = 16 << 10

const (
	sandboxWorkDir    = "/work"
	sandboxModules    = "/modules"
	sandboxBubblewrap = "bwrap"
)

var sandboxBubblewrapPath = func() (string, error) {
	return trustedSystemExecutable(sandboxBubblewrap)
}

var sandboxGitPath = func() (string, error) {
	return trustedSystemExecutable("git")
}

var verifierGoRoot = runtime.GOROOT

// runSandboxedCommand runs one host check in an isolated filesystem and
// network namespace. It never inherits the host environment or home directory.
// The program "go" runs the pinned Go toolchain with the module baseline; any
// other bare executable name is resolved from the trusted system directories
// and runs with the same isolation (the pinned module inputs are still
// provisioned when present, so a project Makefile that calls the Go toolchain
// keeps working offline).
func runSandboxedCommand(ctx context.Context, workDir string, baseline *GoModuleBaseline, policy secretpath.Policy, program string, args ...string) error {
	bwrap, err := sandboxBubblewrapPath()
	if err != nil {
		return hostFailure(fmt.Errorf("bubblewrap is required for workflow verification: %w", err))
	}
	goMode := program == "go"
	if err := validateVerifierProgram(goMode, baseline, program); err != nil {
		return hostFailure(err)
	}
	exePath, toolchainPath, goRoot, err := resolveSandboxExecutable(goMode, baseline, program)
	if err != nil {
		return hostFailure(err)
	}
	tempRoot, err := newSandboxRoot()
	if err != nil {
		return hostFailure(fmt.Errorf("create verifier sandbox: %w", err))
	}
	defer os.RemoveAll(tempRoot)
	copyRoot := filepath.Join(tempRoot, "work")
	if _, err := copySandboxWorktree(workDir, copyRoot, policy); err != nil {
		return hostFailure(err)
	}
	modulesRoot := filepath.Join(tempRoot, "modules")
	var buildCacheRoot string
	if baseline != nil {
		if err := applyGoModuleBaseline(copyRoot, baseline); err != nil {
			return hostFailure(err)
		}
		if err := provisionModuleCache(copyRoot, modulesRoot, baseline, toolchainPath); err != nil {
			return hostFailure(err)
		}
		if buildCacheRoot, err = prepareVerifierBuildCache(baseline); err != nil {
			return hostFailure(err)
		}
	}
	if err := initializeSandboxGit(ctx, copyRoot); err != nil {
		return hostFailure(err)
	}
	homeRoot, err := createSandboxHome(tempRoot)
	if err != nil {
		return hostFailure(err)
	}
	command := exec.CommandContext(ctx, bwrap, sandboxArgs(copyRoot, modulesRoot, homeRoot, goRoot, exePath, buildCacheRoot, baseline != nil, args...)...)
	command.Env = []string{"PATH=/usr/bin:/bin"}
	stdout := newBoundedCapture(maxSandboxCaptureBytes)
	stderr := newBoundedCapture(maxSandboxCaptureBytes)
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return hostFailure(ctx.Err())
		}
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return hostFailure(fmt.Errorf("sandbox command failed: %w", err))
		}
		if strings.HasPrefix(strings.TrimSpace(string(stderr.Bytes())), "bwrap:") {
			return hostFailure(fmt.Errorf("sandbox command failed: %w: %s", err, boundedDiagnostic(stderr.Bytes())))
		}
		return sourceCommandFailure(string(stdout.Bytes())+string(stderr.Bytes()), err)
	}
	return nil
}

func verifierGoToolchain() (string, string, error) {
	root := verifierGoRoot()
	if root == "" || !filepath.IsAbs(root) {
		return "", "", fmt.Errorf("resolve verifier Go root")
	}
	if err := rejectHostHomePath(root); err != nil {
		return "", "", err
	}
	goPath := filepath.Join(root, "bin", "go")
	info, err := os.Stat(goPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return "", "", fmt.Errorf("resolve verifier Go executable")
	}
	return goPath, root, nil
}

func rejectHostHomePath(path string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve verifier host home: %w", err)
	}
	resolvedPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("resolve verifier Go root: %w", err)
	}
	resolvedHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		return fmt.Errorf("resolve verifier host home: %w", err)
	}
	rel, err := filepath.Rel(resolvedHome, resolvedPath)
	if err != nil || rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
		return fmt.Errorf("verifier Go root must not be inside the host home")
	}
	return nil
}

func initializeSandboxGit(ctx context.Context, workRoot string) error {
	git, err := sandboxGitPath()
	if err != nil {
		return fmt.Errorf("resolve verifier Git executable: %w", err)
	}
	command := exec.CommandContext(ctx, git, "-c", "init.templateDir=", "init", "--quiet", workRoot)
	command.Env = []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("initialize verifier Git worktree: %w: %s", err, boundedDiagnostic(output))
	}
	return nil
}

func trustedSystemExecutable(name string) (string, error) {
	for _, directory := range []string{"/usr/bin", "/bin"} {
		path := filepath.Join(directory, name)
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
			continue
		}
		return path, nil
	}
	return "", fmt.Errorf("trusted system executable %q is unavailable", name)
}

func boundedDiagnostic(output []byte) string {
	text := redact.Text(string(output))
	if len(text) > maxVerifierDiagnosticBytes {
		return text[:maxVerifierDiagnosticBytes] + "\n[diagnostic truncated]"
	}
	if strings.TrimSpace(text) == "" {
		return "source check failed without diagnostic output"
	}
	return text
}

func sandboxArgs(workRoot, modulesRoot, homeRoot, goRoot, exePath, buildCacheRoot string, goEnv bool, args ...string) []string {
	result := []string{
		"--unshare-all", "--die-with-parent", "--new-session", "--clearenv",
		"--ro-bind", "/usr", "/usr",
		"--ro-bind", "/bin", "/bin",
		"--ro-bind", "/lib", "/lib",
		"--ro-bind", "/lib64", "/lib64",
	}
	if goEnv {
		if !strings.HasPrefix(goRoot, "/usr/") && goRoot != "/usr" {
			result = append(result, "--ro-bind", goRoot, goRoot)
		}
	}
	result = append(result,
		"--bind", workRoot, sandboxWorkDir,
		"--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp", "--tmpfs", "/home", "--bind", homeRoot, "/home/sandbox",
		"--chdir", sandboxWorkDir,
		"--setenv", "PATH", "/usr/bin:/bin",
		"--setenv", "HOME", "/home/sandbox",
		"--setenv", "TMPDIR", "/tmp",
	)
	if goEnv {
		result = append(result,
			"--ro-bind", modulesRoot, sandboxModules,
			"--bind", buildCacheRoot, "/gocache",
			"--setenv", "GOCACHE", "/gocache",
			"--setenv", "GOMODCACHE", sandboxModules,
			"--setenv", "GOWORK", "off",
			"--setenv", "GOPROXY", "off",
			"--setenv", "GOSUMDB", "off",
			"--setenv", "GIT_CONFIG_NOSYSTEM", "1",
			"--setenv", "GIT_CONFIG_GLOBAL", "/dev/null",
		)
	}
	result = append(result,
		"--", exePath,
	)
	return append(result, args...)
}

func createSandboxHome(tempRoot string) (string, error) {
	homeRoot := filepath.Join(tempRoot, "home")
	configDir := workspace.NamespacePath(homeRoot)
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return "", fmt.Errorf("create verifier home: %w", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, ".env"), nil, 0o600); err != nil {
		return "", fmt.Errorf("create verifier env file: %w", err)
	}
	return homeRoot, nil
}
