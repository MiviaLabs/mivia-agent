package tools

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// This file pins the extracted run_command command gate: the free function
// resolveAllowedCommand(argv, allowlist) that resolveCommand delegates to so
// run_command behavior stays byte-identical. Every rejection here asserts the
// exact error text the tool surfaces through Execute, and every membership
// case asserts the case-folded full-name / base-name rules the gate uses.
// Changing any of these strings or the check order is a contract change and
// must be deliberate.

// TestResolveAllowedCommandEmptyArgvRejected pins gate check 1: an empty argv
// (nil or zero-length) is rejected before the allowlist or LookPath is ever
// consulted, with the exact error text.
func TestResolveAllowedCommandEmptyArgvRejected(t *testing.T) {
	const want = "argv must be non-empty"
	for _, argv := range [][]string{nil, {}} {
		_, _, err := resolveAllowedCommand(argv, []string{"git"})
		if err == nil {
			t.Fatalf("argv=%v: expected rejection", argv)
		}
		if err.Error() != want {
			t.Fatalf("argv=%v: error=%q, want exact %q", argv, err.Error(), want)
		}
	}
}

// TestResolveAllowedCommandPathShapedArgvRejected pins gate check 2: argv[0]
// carrying a path separator (forward slash, backslash, or the platform
// separator) is rejected regardless of allowlist membership, with the exact
// error text.
func TestResolveAllowedCommandPathShapedArgvRejected(t *testing.T) {
	cases := []struct {
		name string
		bin  string
	}{
		{"dot-slash relative", "./hello"},
		{"subdirectory relative", "bin/x"},
		{"windows backslash", `bin\x`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Membership is irrelevant: the path check fires first, so even an
			// allowlist naming the same string must not admit it.
			_, _, err := resolveAllowedCommand([]string{tc.bin}, []string{tc.bin})
			if err == nil {
				t.Fatalf("argv[0]=%q: expected path rejection", tc.bin)
			}
			want := fmt.Sprintf("program must be a bare name on the allowlist, not a path: %q", tc.bin)
			if err.Error() != want {
				t.Fatalf("argv[0]=%q: error=%q, want exact %q", tc.bin, err.Error(), want)
			}
		})
	}
}

// TestResolveAllowedCommandCaseFoldedFullNameMembership pins gate check 3:
// argv[0] is folded to lower case for membership, so an uppercase program name
// is a full-name member of a lowercase allowlist entry. The fold is applied to
// the argv side: "MAKE" matches allowlist entry "make".
func TestResolveAllowedCommandCaseFoldedFullNameMembership(t *testing.T) {
	if !allowed("MAKE", []string{"make"}) {
		t.Fatal(`allowed("MAKE", ["make"]) must be true (case-folded full-name membership)`)
	}
	if !allowed("Git", []string{"git"}) {
		t.Fatal(`allowed("Git", ["git"]) must be true (mixed case folds too)`)
	}
	// End-to-end through the full gate: MAKE passes membership, so the only
	// remaining failure can be LookPath (case-sensitive hosts have no "MAKE"
	// binary) or success (case-insensitive hosts). It must never be reported
	// as not allowlisted.
	_, _, err := resolveAllowedCommand([]string{"MAKE"}, []string{"make"})
	if err != nil && strings.Contains(err.Error(), "not allowlisted") {
		t.Fatalf("MAKE must be a case-folded member of [make]; gate said: %v", err)
	}
}

// TestResolveAllowedCommandBaseNameMembership pins gate check 4: the
// membership helper also matches the base name of a full path (filepath.Base),
// so an allowlist naming "lint" covers a binary at "/usr/bin/lint". The gate
// itself rejects path-shaped argv[0] before membership is consulted, so this
// property is pinned on the helper and the precedence is pinned end-to-end.
func TestResolveAllowedCommandBaseNameMembership(t *testing.T) {
	if !allowed("/usr/bin/lint", []string{"lint"}) {
		t.Fatal(`allowed("/usr/bin/lint", ["lint"]) must be true (base-name membership)`)
	}
	if !allowed("/usr/bin/Lint", []string{"lint"}) {
		t.Fatal(`allowed("/usr/bin/Lint", ["lint"]) must be true (base name folds case too)`)
	}
	// Precedence: a path-shaped argv is rejected as a path and never reaches
	// the membership check.
	_, _, err := resolveAllowedCommand([]string{"/usr/bin/lint"}, []string{"lint"})
	if err == nil {
		t.Fatal("path-shaped argv must be rejected by the gate")
	}
	if !strings.Contains(err.Error(), "bare name") {
		t.Fatalf("path rejection must precede membership, got: %v", err)
	}
}

// TestResolveAllowedCommandNotAllowlistedError pins gate check 5: a bare name
// absent from the allowlist is rejected with the exact error text, naming the
// allowed programs exactly as strings.Join(allowlist, ", ") renders them - the
// same list the run_command tool description promises.
func TestResolveAllowedCommandNotAllowlistedError(t *testing.T) {
	_, _, err := resolveAllowedCommand([]string{"sudo"}, []string{"git", "make"})
	if err == nil {
		t.Fatal("expected not-allowlisted rejection")
	}
	want := `program "sudo" is not allowlisted (allowed: git, make)`
	if err.Error() != want {
		t.Fatalf("error=%q, want exact %q", err.Error(), want)
	}
}

// TestResolveAllowedCommandLookPathFailure pins gate check 6: a bare name that
// passes membership but cannot be resolved on PATH fails with the exact
// "program not found on PATH: %s" text, with the name verbatim (not %q-quoted).
func TestResolveAllowedCommandLookPathFailure(t *testing.T) {
	const missing = "definitely-not-a-real-binary-zz9"
	_, _, err := resolveAllowedCommand([]string{missing}, []string{missing})
	if err == nil {
		t.Fatal("expected LookPath failure")
	}
	want := "program not found on PATH: " + missing
	if err.Error() != want {
		t.Fatalf("error=%q, want exact %q", err.Error(), want)
	}
}

// TestResolveAllowedCommandWindowsShellBuiltins pins gate check 7 (Windows):
// echo/true/false are shell builtins, not executables, on Windows, so even an
// allowlisted argv[0] of that name is refused before LookPath with the exact
// error text. Mirrors run.go's runtime.GOOS gate and the repo's existing
// "skip unless Windows" test pattern.
func TestResolveAllowedCommandWindowsShellBuiltins(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-only gate: echo/true/false are shell builtins there")
	}
	for _, name := range []string{"echo", "true", "false"} {
		_, _, err := resolveAllowedCommand([]string{name}, []string{name})
		if err == nil {
			t.Fatalf("%s: expected Windows shell-builtin rejection", name)
		}
		want := fmt.Sprintf("program %q is not available without a shell on Windows", name)
		if err.Error() != want {
			t.Fatalf("%s: error=%q, want exact %q", name, err.Error(), want)
		}
	}
}

// TestResolveAllowedCommandWindowsGateInactiveOnUnix is the complement of the
// Windows gate: on Unix, echo/true/false are ordinary executables, so the gate
// must never fire there - a future change must not break them outside Windows.
func TestResolveAllowedCommandWindowsGateInactiveOnUnix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix-only complement to the Windows shell-builtin gate")
	}
	for _, name := range []string{"echo", "true", "false"} {
		_, _, err := resolveAllowedCommand([]string{name}, []string{name})
		if err != nil && strings.Contains(err.Error(), "not available without a shell on Windows") {
			t.Fatalf("%s: Windows gate must not fire on %s: %v", name, runtime.GOOS, err)
		}
	}
}

// TestResolveAllowedCommandResolvesOnPath pins the happy path: a bare,
// allowlisted name resolves via LookPath to an absolute binary, and the
// remaining argv (argv[1:]) is returned untouched for the process arguments.
func TestResolveAllowedCommandResolvesOnPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh is not the Unix shell path on Windows")
	}
	bin, rest, err := resolveAllowedCommand([]string{"sh", "-c", "echo hi"}, []string{"sh"})
	if err != nil {
		t.Fatalf("resolve sh: %v", err)
	}
	if bin == "" || !filepath.IsAbs(bin) {
		t.Fatalf("resolved binary must be an absolute path, got %q", bin)
	}
	if len(rest) != 2 || rest[0] != "-c" || rest[1] != "echo hi" {
		t.Fatalf("remaining argv = %v, want [-c echo hi]", rest)
	}
}

// TestResolveAllowedCommandResolveCommandDelegates pins that the tool method
// resolveCommand is byte-identical to the extracted gate: the same rejections
// and the same error text flow out of the tool's Execute path.
func TestResolveAllowedCommandResolveCommandDelegates(t *testing.T) {
	tool := &runCommandTool{allowlist: []string{"git"}}

	_, _, err := tool.resolveCommand([]string{"sudo"})
	if err == nil {
		t.Fatal("expected not-allowlisted rejection via resolveCommand")
	}
	if want := `program "sudo" is not allowlisted (allowed: git)`; err.Error() != want {
		t.Fatalf("resolveCommand error=%q, want exact %q", err.Error(), want)
	}

	_, _, err = tool.resolveCommand(nil)
	if err == nil || err.Error() != "argv must be non-empty" {
		t.Fatalf("resolveCommand(nil) error=%v, want exact %q", err, "argv must be non-empty")
	}
}
