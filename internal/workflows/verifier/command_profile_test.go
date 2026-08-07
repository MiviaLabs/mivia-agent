package verifier

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandProfileRejectsBadDeclarations(t *testing.T) {
	tests := []struct{ name, check, program string }{
		{"empty check", "", "make"},
		{"empty program", "c", ""},
		{"path program", "c", "/usr/bin/make"},
		{"relative path program", "c", "./make"},
		{"shell metachar program", "c", "make; rm -rf /"},
		{"space program", "c", "my tool"},
		{"dot program", "c", "."},
		{"dotdot program", "c", ".."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewCommandProfile(tc.check, tc.program, nil); err == nil {
				t.Fatalf("NewCommandProfile(%q, %q) accepted an unsafe declaration", tc.check, tc.program)
			}
		})
	}
}

func TestCommandProfileVerifyPassesAndFailsWithEvidence(t *testing.T) {
	t.Run("passed", func(t *testing.T) {
		profile, err := NewCommandProfile("invariants", "make", []string{"invariants"})
		if err != nil {
			t.Fatal(err)
		}
		cp := profile.(*CommandProfile)
		cp.run = func(context.Context, string, string, ...string) error { return nil }
		result, err := cp.Verify(context.Background(), Request{WorkDir: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		if result.Status != "passed" || len(result.Checks) != 1 || result.Checks[0].Status != "passed" {
			t.Fatalf("passed result = %#v", result)
		}
	})

	t.Run("source failure is repairable with detail", func(t *testing.T) {
		profile, err := NewCommandProfile("invariants", "make", []string{"invariants"})
		if err != nil {
			t.Fatal(err)
		}
		cp := profile.(*CommandProfile)
		cp.run = func(context.Context, string, string, ...string) error {
			return &commandFailure{class: "source", detail: "manifest has stale references", err: errors.New("source check failed")}
		}
		result, err := cp.Verify(context.Background(), Request{WorkDir: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		if result.Status != "failed" || len(result.Checks) != 1 {
			t.Fatalf("failed result = %#v", result)
		}
		check := result.Checks[0]
		if check.Status != "failed" || check.Class != "source" || check.Detail != "manifest has stale references" {
			t.Fatalf("check = %#v, want failed source check with detail", check)
		}
		if !result.Repairable() {
			t.Fatalf("source failure must be repairable: %#v", result)
		}
	})

	t.Run("host failure is not repairable", func(t *testing.T) {
		profile, err := NewCommandProfile("invariants", "make", []string{"invariants"})
		if err != nil {
			t.Fatal(err)
		}
		cp := profile.(*CommandProfile)
		cp.run = func(context.Context, string, string, ...string) error {
			return hostFailure(errors.New("bubblewrap is unavailable"))
		}
		result, err := cp.Verify(context.Background(), Request{WorkDir: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Checks) != 1 || result.Checks[0].Class != "host" {
			t.Fatalf("host failure result = %#v", result)
		}
		if result.Repairable() {
			t.Fatalf("host failure must not be repairable: %#v", result)
		}
	})
}

// TestCommandProfileRunsNonGoProgramInSandbox exercises the generic sandbox
// path with a real system binary behind the stubbed bubblewrap passthrough:
// the declared bare executable is resolved from the trusted system
// directories and its argv is passed verbatim, with the same isolation as the
// fixed Go profiles.
func TestCommandProfileRunsNonGoProgramInSandbox(t *testing.T) {
	if _, err := trustedSystemExecutable("python3"); err != nil {
		t.Skipf("python3 is not available in the trusted system directories: %v", err)
	}
	stubBubblewrapPath(t, writeFakeBwrapPassthrough(t))
	stubGitPath(t, writeFakeGit(t))

	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "gate.txt"), []byte("project file"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("passing command", func(t *testing.T) {
		// The stubbed bubblewrap passthrough does not implement --chdir, so the
		// command must not depend on the sandbox working directory; this
		// asserts the declared argv is passed verbatim to the resolved binary.
		profile, err := NewCommandProfile("gate", "python3", []string{"-c", "import sys; assert sys.argv[1:] == ['--flag', 'value']", "--flag", "value"})
		if err != nil {
			t.Fatal(err)
		}
		result, err := profile.Verify(context.Background(), Request{WorkDir: workDir})
		if err != nil {
			t.Fatal(err)
		}
		if result.Status != "passed" || len(result.Checks) != 1 || result.Checks[0].Status != "passed" {
			t.Fatalf("result = %#v, want passed", result)
		}
	})

	t.Run("failing command yields source evidence", func(t *testing.T) {
		profile, err := NewCommandProfile("gate", "python3", []string{"-c", "import sys; print('stale reference: TestOld'); sys.exit(1)"})
		if err != nil {
			t.Fatal(err)
		}
		result, err := profile.Verify(context.Background(), Request{WorkDir: workDir})
		if err != nil {
			t.Fatal(err)
		}
		if result.Status != "failed" || len(result.Checks) != 1 {
			t.Fatalf("result = %#v, want failed", result)
		}
		check := result.Checks[0]
		if check.Class != "source" {
			t.Fatalf("check class = %q, want source (detail %q)", check.Class, check.Detail)
		}
		if !strings.Contains(check.Detail, "stale reference: TestOld") {
			t.Fatalf("check detail %q must carry the command output", check.Detail)
		}
		if !result.Repairable() {
			t.Fatalf("source failure must be repairable: %#v", result)
		}
	})
}

// TestCommandProfileMissingProgramIsHostFailure verifies that a declared
// program that is not a trusted system executable fails closed as a host
// failure (never repairable, never dispatched).
func TestCommandProfileMissingProgramIsHostFailure(t *testing.T) {
	stubBubblewrapPath(t, writeFakeBwrapPassthrough(t))
	stubGitPath(t, writeFakeGit(t))
	profile, err := NewCommandProfile("gate", "definitely-not-a-real-tool-9f3a", nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := profile.Verify(context.Background(), Request{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Checks) != 1 || result.Checks[0].Class != "host" {
		t.Fatalf("result = %#v, want host failure", result)
	}
	if result.Repairable() {
		t.Fatalf("host failure must not be repairable: %#v", result)
	}
	if result.Checks[0].Detail == "" {
		t.Fatal("host failure must carry a diagnostic detail")
	}
}

func TestCommandProfileName(t *testing.T) {
	profile, err := NewCommandProfile("gate", "python3", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := profile.Name(); got != "command:python3" {
		t.Fatalf("Name() = %q, want command:python3", got)
	}
	if got := fmt.Sprintf("%v", profile); got == "" {
		t.Fatal("profile must stringify")
	}
}
