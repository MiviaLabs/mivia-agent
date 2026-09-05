package definition

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestVerifierGoToolchainMissingBinaryIsAnError pins the error path for a
// GOROOT that exists but carries no bin/go. os.Stat returns a NIL FileInfo
// with its error, so reading info.Mode() before checking err panicked the
// whole process - and nothing on the evidence-gate path recovers, so a
// stripped toolchain image took down the run instead of failing the gate.
func TestVerifierGoToolchainMissingBinaryIsAnError(t *testing.T) {
	orig := verifierGoRoot
	verifierGoRoot = func() string { return t.TempDir() }
	t.Cleanup(func() { verifierGoRoot = orig })

	goPath, root, err := verifierGoToolchain()
	if err == nil {
		t.Fatalf("verifierGoToolchain() = (%q, %q, nil), want an error", goPath, root)
	}
	if !strings.Contains(err.Error(), "resolve verifier Go executable") {
		t.Fatalf("verifierGoToolchain() error = %q, want it to name the executable", err)
	}
}

// TestVerifierGoToolchainRejectsNonExecutable keeps the x-bit check alive on
// unix: a regular file at bin/go that nothing can execute is not a toolchain.
func TestVerifierGoToolchainRejectsNonExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows Stat reports no execute bit, so the x-bit check is GOOS-gated")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "bin", "go"), []byte("not a binary"), 0o644); err != nil {
		t.Fatalf("write bin/go: %v", err)
	}
	orig := verifierGoRoot
	verifierGoRoot = func() string { return root }
	t.Cleanup(func() { verifierGoRoot = orig })

	if _, _, err := verifierGoToolchain(); err == nil {
		t.Fatal("verifierGoToolchain() accepted a non-executable bin/go")
	}
}

// TestVerifierGoToolchainAcceptsExecutable proves the happy path still
// resolves, so the error-ordering fix above did not close the door on a real
// toolchain.
func TestVerifierGoToolchainAcceptsExecutable(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	name := "go"
	if runtime.GOOS == "windows" {
		name = "go.exe"
	}
	goPath := filepath.Join(root, "bin", name)
	if err := os.WriteFile(goPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write bin/%s: %v", name, err)
	}
	orig := verifierGoRoot
	verifierGoRoot = func() string { return root }
	t.Cleanup(func() { verifierGoRoot = orig })

	gotPath, gotRoot, err := verifierGoToolchain()
	if err != nil {
		t.Fatalf("verifierGoToolchain() = %v, want the resolved toolchain", err)
	}
	if gotPath != goPath || gotRoot != root {
		t.Fatalf("verifierGoToolchain() = (%q, %q), want (%q, %q)", gotPath, gotRoot, goPath, root)
	}
}

// TestSandboxArgsTolerateMissingArchLibDirs pins /lib and /lib64 as TOLERANT
// binds. bwrap treats a missing --ro-bind source as a hard error, so an
// unconditional /lib64 bind failed every evidence gate on hosts that have no
// /lib64 (arm64 Debian and Ubuntu, Alpine and musl, minimal containers). The
// failure classified host-class and therefore non-repairable, so the run
// stalled with no path forward. /usr and /bin stay REQUIRED: the sandbox sets
// PATH=/usr/bin:/bin, so a host missing those cannot run anything.
func TestSandboxArgsTolerateMissingArchLibDirs(t *testing.T) {
	args := sandboxArgs("/work", "/mods", "/home-root", "/usr/lib/go", "/usr/bin/true", "/cache", true, "go", "test")

	bindKind := func(target string) string {
		for i := 0; i+2 < len(args); i++ {
			if args[i+2] == target && (args[i] == "--ro-bind" || args[i] == "--ro-bind-try") {
				return args[i]
			}
		}
		return ""
	}

	for _, target := range []string{"/lib", "/lib64"} {
		if got := bindKind(target); got != "--ro-bind-try" {
			t.Errorf("bind for %s = %q, want --ro-bind-try so a host without it still runs", target, got)
		}
	}
	for _, target := range []string{"/usr", "/bin"} {
		if got := bindKind(target); got != "--ro-bind" {
			t.Errorf("bind for %s = %q, want a required --ro-bind", target, got)
		}
	}
}
