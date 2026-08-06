package verifier

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/redact"
	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
)

const maxVerifierDiagnosticBytes = 16 << 10

type commandFailure struct {
	class  string
	detail string
	err    error
}

func (e *commandFailure) Error() string { return e.err.Error() }
func (e *commandFailure) Unwrap() error { return e.err }

const (
	sandboxWorkDir    = "/work"
	sandboxModules    = "/modules"
	sandboxBubblewrap = "bwrap"
)

var sandboxBubblewrapPath = func() (string, error) {
	return exec.LookPath(sandboxBubblewrap)
}

// runSandboxedCommand runs a fixed host check in an isolated filesystem and
// network namespace. It never inherits the host environment or home directory.
func runSandboxedCommand(ctx context.Context, workDir string, baseline *GoModuleBaseline, program string, args ...string) error {
	if program != "go" {
		return hostFailure(fmt.Errorf("sandbox rejects fixed program %q", program))
	}
	bwrap, err := sandboxBubblewrapPath()
	if err != nil {
		return hostFailure(fmt.Errorf("bubblewrap is required for workflow verification: %w", err))
	}
	tempRoot, err := os.MkdirTemp("", "mivia-verifier-")
	if err != nil {
		return hostFailure(fmt.Errorf("create verifier sandbox: %w", err))
	}
	defer os.RemoveAll(tempRoot)
	copyRoot := filepath.Join(tempRoot, "work")
	if _, err := copySandboxWorktree(workDir, copyRoot); err != nil {
		return hostFailure(err)
	}
	modulesRoot := filepath.Join(tempRoot, "modules")
	if err := applyGoModuleBaseline(copyRoot, baseline); err != nil {
		return hostFailure(err)
	}
	if err := provisionModuleCache(copyRoot, modulesRoot, baseline); err != nil {
		return hostFailure(err)
	}
	command := exec.CommandContext(ctx, bwrap, sandboxArgs(copyRoot, modulesRoot, program, args...)...)
	command.Env = []string{"PATH=/usr/bin:/bin"}
	output, err := command.CombinedOutput()
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return hostFailure(ctx.Err())
	}
	detail := boundedDiagnostic(output)
	if strings.HasPrefix(strings.TrimSpace(detail), "bwrap:") {
		return hostFailure(fmt.Errorf("sandbox command failed: %w", err))
	}
	return &commandFailure{class: "source", detail: detail, err: fmt.Errorf("source check failed: %w", err)}
}

func hostFailure(err error) *commandFailure {
	return &commandFailure{class: "host", detail: "host verifier setup failed", err: err}
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

func sandboxArgs(workRoot, modulesRoot, program string, args ...string) []string {
	result := []string{
		"--unshare-all", "--die-with-parent", "--new-session", "--clearenv",
		"--ro-bind", "/usr", "/usr",
		"--ro-bind", "/bin", "/bin",
		"--ro-bind", "/lib", "/lib",
		"--ro-bind", "/lib64", "/lib64",
		"--bind", workRoot, sandboxWorkDir,
		"--ro-bind", modulesRoot, sandboxModules,
		"--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp", "--tmpfs", "/home",
		"--chdir", sandboxWorkDir,
		"--setenv", "PATH", "/usr/bin:/bin",
		"--setenv", "HOME", "/home/sandbox",
		"--setenv", "TMPDIR", "/tmp",
		"--setenv", "GOCACHE", "/tmp/go-cache",
		"--setenv", "GOMODCACHE", sandboxModules,
		"--setenv", "GOWORK", "off",
		"--setenv", "GOPROXY", "off",
		"--setenv", "GOSUMDB", "off",
		"--setenv", "GIT_CONFIG_NOSYSTEM", "1",
		"--setenv", "GIT_CONFIG_GLOBAL", "/dev/null",
		"--", program,
	}
	return append(result, args...)
}

func copySandboxWorktree(source, destination string) (string, error) {
	info, err := os.Stat(source)
	if err != nil {
		return "", fmt.Errorf("inspect verifier worktree: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("verifier worktree is not a directory")
	}
	if err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(destination, 0o700)
		}
		if rel == ".git" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if sandboxSecretPath(rel) {
			return fmt.Errorf("verifier worktree contains a secret-like file")
		}
		target := filepath.Join(destination, rel)
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("verifier worktree contains symlink %q", rel)
		}
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("verifier worktree contains unsupported file %q", rel)
		}
		return copyRegularFile(path, target)
	}); err != nil {
		return "", fmt.Errorf("copy verifier worktree: %w", err)
	}
	return destination, nil
}

func sandboxSecretPath(rel string) bool {
	base := strings.ToLower(filepath.Base(rel))
	if base == ".env" || strings.HasPrefix(base, ".env.") {
		return base != ".env.example"
	}
	if base == ".npmrc" || base == ".netrc" || base == ".pypirc" || base == ".dockercfg" || base == "credentials.json" {
		return true
	}
	if strings.Contains(base, "credential") || strings.Contains(base, "secret") || strings.Contains(base, "token") || strings.Contains(base, "password") || strings.Contains(base, "passwd") {
		return true
	}
	return strings.HasSuffix(base, ".pem") || strings.HasSuffix(base, ".key") || strings.HasSuffix(base, ".p12") || strings.HasSuffix(base, ".pfx") || strings.HasSuffix(base, ".jks") || base == "id_rsa" || base == "id_ed25519"
}

// CaptureGoModuleBaseline reads the module inputs before workflow execution.
func CaptureGoModuleBaseline(workRoot string) (*GoModuleBaseline, error) {
	goMod, err := os.ReadFile(filepath.Join(workRoot, "go.mod"))
	if err != nil {
		return nil, fmt.Errorf("read verifier go.mod: %w", err)
	}
	goSum, err := os.ReadFile(filepath.Join(workRoot, "go.sum"))
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("read verifier go.sum: %w", err)
	}
	return &GoModuleBaseline{GoMod: goMod, GoSum: goSum}, nil
}

func applyGoModuleBaseline(workRoot string, baseline *GoModuleBaseline) error {
	if baseline == nil || len(baseline.GoMod) == 0 {
		return fmt.Errorf("workflow verifier module baseline is missing")
	}
	goModPath := filepath.Join(workRoot, "go.mod")
	currentGoMod, err := os.ReadFile(goModPath)
	if err != nil {
		return fmt.Errorf("read verifier go.mod: %w", err)
	}
	if !bytes.Equal(currentGoMod, baseline.GoMod) {
		return fmt.Errorf("workflow changed go.mod after admission")
	}
	goSumPath := filepath.Join(workRoot, "go.sum")
	if len(baseline.GoSum) == 0 {
		if _, err := os.Stat(goSumPath); err == nil {
			return fmt.Errorf("workflow changed go.sum after admission")
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect verifier go.sum: %w", err)
		}
		return nil
	}
	currentGoSum, err := os.ReadFile(goSumPath)
	if err != nil {
		return fmt.Errorf("read verifier go.sum: %w", err)
	}
	if !bytes.Equal(currentGoSum, baseline.GoSum) {
		return fmt.Errorf("workflow changed go.sum after admission")
	}
	return nil
}

func provisionModuleCache(workRoot, destination string, baseline *GoModuleBaseline) error {
	if baseline == nil {
		return fmt.Errorf("workflow verifier module baseline is missing")
	}
	parsed, err := modfile.Parse("go.mod", baseline.GoMod, nil)
	if err != nil {
		return fmt.Errorf("parse verifier go.mod: %w", err)
	}
	if len(parsed.Replace) != 0 {
		return fmt.Errorf("verifier rejects Go module replacements")
	}
	cacheRoot, err := hostModuleCache()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return fmt.Errorf("create verifier module cache: %w", err)
	}
	for _, require := range parsed.Require {
		if err := copyRequiredModule(cacheRoot, destination, require.Mod.Path, require.Mod.Version); err != nil {
			return err
		}
	}
	return nil
}

func hostModuleCache() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve host home for Go module cache: %w", err)
	}
	command := exec.Command("go", "env", "GOMODCACHE")
	command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + home}
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("resolve host Go module cache: %w", err)
	}
	cacheRoot := strings.TrimSpace(string(output))
	if cacheRoot == "" || !filepath.IsAbs(cacheRoot) {
		return "", fmt.Errorf("host Go module cache is invalid")
	}
	return cacheRoot, nil
}

func copyRequiredModule(cacheRoot, destination, modulePath, version string) error {
	escapedPath, err := module.EscapePath(modulePath)
	if err != nil {
		return fmt.Errorf("escape module path %q: %w", modulePath, err)
	}
	escapedVersion, err := module.EscapeVersion(version)
	if err != nil {
		return fmt.Errorf("escape module version %q: %w", version, err)
	}
	source := filepath.Join(cacheRoot, escapedPath+"@"+escapedVersion)
	target := filepath.Join(destination, escapedPath+"@"+escapedVersion)
	if _, err := copySandboxWorktree(source, target); err != nil {
		return fmt.Errorf("provision module %s@%s: %w", modulePath, version, err)
	}
	return nil
}

func copyRegularFile(source, destination string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}
