package verifier

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/secretpath"
	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
)

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

func provisionModuleCache(workRoot, destination string, baseline *GoModuleBaseline, goPath string) error {
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
	cacheRoot, err := hostModuleCache(goPath)
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

func hostModuleCache(goPath string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve host home for Go module cache: %w", err)
	}
	command := exec.Command(goPath, "env", "GOMODCACHE")
	// `go env GOMODCACHE` defaults to $HOME/go/pkg/mod only when GOPATH is
	// unset. Preserve the host GOPATH (and an explicit GOMODCACHE) so a host
	// whose Go paths live outside the home directory - a CI runner with
	// GOPATH=/home/runner/go, or a developer with a custom GOPATH - reports
	// the cache the modules were actually downloaded into, not a phantom
	// directory under the sandboxed home.
	command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + home}
	for _, key := range []string{"GOPATH", "GOMODCACHE"} {
		if value := os.Getenv(key); value != "" {
			command.Env = append(command.Env, key+"="+value)
		}
	}
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
	// The extracted source tree only exists for modules the host build
	// actually compiled. With pruned module graphs a host legitimately lacks
	// it for modules that provide no packages for the host platform (a
	// windows-only dependency on a Linux machine is never downloaded), so a
	// missing tree is not a provision failure: the sandboxed command itself
	// reports go's normal missing-module error if it truly needs the source.
	source := filepath.Join(cacheRoot, escapedPath+"@"+escapedVersion)
	target := filepath.Join(destination, escapedPath+"@"+escapedVersion)
	if _, statErr := os.Stat(source); statErr == nil {
		if _, err := copySandboxTree(source, target, secretpath.Policy{}); err != nil {
			return fmt.Errorf("provision module %s@%s: %w", modulePath, version, err)
		}
	}
	return copyRequiredModuleMetadata(cacheRoot, destination, escapedPath, escapedVersion)
}

func copyRequiredModuleMetadata(cacheRoot, destination, escapedPath, escapedVersion string) error {
	sourceBase := filepath.Join(cacheRoot, "cache", "download", escapedPath, "@v", escapedVersion)
	targetBase := filepath.Join(destination, "cache", "download", escapedPath, "@v", escapedVersion)
	if err := os.MkdirAll(filepath.Dir(targetBase), 0o700); err != nil {
		return fmt.Errorf("create verifier module metadata: %w", err)
	}
	// Each artifact is copied when the host cache has it. The .mod file is
	// what a pruned graph needs for every module; .zip/.ziphash exist only
	// for modules whose source the host build fetched. A missing artifact is
	// left for the sandboxed command to report in go's own terms.
	for _, suffix := range []string{".info", ".mod", ".zip", ".ziphash"} {
		source := sourceBase + suffix
		if _, statErr := os.Stat(source); statErr != nil {
			continue
		}
		if err := copyRegularFile(source, targetBase+suffix); err != nil {
			return fmt.Errorf("copy verifier module metadata %s: %w", suffix, err)
		}
	}
	return nil
}
