package config

import (
	"strings"
	"testing"
)

// diagnostics_config_test.go is the RED-phase contract for [tools]
// diagnostics_command (locked plan v2, task c1). The config layer must
// validate the get_diagnostics command the way the tools layer's run_command
// gate does (internal/tools/run.go resolveAllowedCommand): argv[0] must be a
// bare name on the resolved run allowlist. Bad values are LOAD errors - a
// config whose diagnostics tool could never register must not load clean.
//
// STE: this file compiles against the CURRENT config package, which has no
// DiagnosticsCommand field and no diagnostics validation, and it must FAIL its
// error-case assertions: the lenient TOML decoder silently drops the unknown
// diagnostics_command key, so Load currently succeeds where the contract says
// it must error. The tests drive the surface through the shared loading
// helper (writeMinimalConfig) instead of constructing ToolsConfig, so they
// stay buildable until the field exists.

// STE: a path-shaped argv[0] ("/bin/sh", "C:\Tools\diag.exe") is never a bare
// name on the allowlist; run_command's gate refuses path-shaped argv[0], so
// the config layer must refuse the same value at load instead of shipping a
// diagnostics command that can never run.
func TestDiagnosticsConfigPathShapedArgv0IsLoadError(t *testing.T) {
	// tomlArgv0 is the value exactly as it must appear in the TOML document
	// (backslashes doubled for the basic-string escape rules); the decoded
	// argv[0] is the unescaped path.
	cases := []struct {
		name      string
		tomlArgv0 string
		argv0     string
	}{
		{name: "posix path", tomlArgv0: `"/bin/sh"`, argv0: "/bin/sh"},
		{name: "windows path", tomlArgv0: `"C:\\Tools\\diag.exe"`, argv0: `C:\Tools\diag.exe`},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			path := writeMinimalConfig(t, `[tools]
diagnostics_command = [`+tt.tomlArgv0+`, "-c", "echo hi"]
`)
			if _, err := Load(LoadOptions{ConfigPath: path}); err == nil {
				t.Fatalf("Load accepted a path-shaped diagnostics_command argv[0] %q; want a load error", tt.argv0)
			} else if !strings.Contains(err.Error(), "diagnostics_command") {
				t.Fatalf("load error must name diagnostics_command, got: %v", err)
			}
		})
	}
}

// STE: argv[0] must be on the resolved run allowlist. Nothing is compiled into
// the binary, so with run_allowlist = ["git"], "node" is not allowlisted and a
// diagnostics command that could never run must be a load error, not a
// silently absent tool.
func TestDiagnosticsConfigArgv0NotInRunAllowlistIsValidationError(t *testing.T) {
	path := writeMinimalConfig(t, `[tools]
run_allowlist = ["git"]
diagnostics_command = ["node", "--version"]
`)
	if _, err := Load(LoadOptions{ConfigPath: path}); err == nil {
		t.Fatal("Load accepted a diagnostics_command whose argv[0] is not in run_allowlist; want a validation error")
	} else if !strings.Contains(err.Error(), "diagnostics_command") {
		t.Fatalf("load error must name diagnostics_command, got: %v", err)
	}
}

// STE: an allowlisted bare-name argv[0] must pass validation so the
// get_diagnostics tool can register at runtime.
func TestDiagnosticsConfigArgv0InRunAllowlistPassesValidation(t *testing.T) {
	path := writeMinimalConfig(t, `[tools]
run_allowlist = ["sh"]
diagnostics_command = ["sh", "-c", "echo hi"]
`)
	if _, err := Load(LoadOptions{ConfigPath: path}); err != nil {
		t.Fatalf("Load rejected an allowlisted diagnostics_command: %v", err)
	}
}

// STE: unset or empty diagnostics_command keeps every existing config loading:
// the get_diagnostics tool is simply not registered. Backward compatibility is
// a hard contract - validation must be a no-op on the zero value.
func TestDiagnosticsConfigEmptyPassesBackwardCompatible(t *testing.T) {
	// Key entirely absent (no [tools] table at all).
	if _, err := Load(LoadOptions{ConfigPath: writeMinimalConfig(t, "")}); err != nil {
		t.Fatalf("config without diagnostics_command rejected: %v", err)
	}
	// Key present but empty.
	path := writeMinimalConfig(t, `[tools]
diagnostics_command = []
`)
	if _, err := Load(LoadOptions{ConfigPath: path}); err != nil {
		t.Fatalf("empty diagnostics_command rejected: %v", err)
	}
}

// STE: run_allowlist_only replaces run_allowlist entirely (resolveToolsConfig
// clears RunAllowlist when RunAllowlistOnly is set, before Validate runs), so
// membership must be checked against run_allowlist_only alone when it is set:
// a command allowlisted only in the non-authoritative run_allowlist must be
// refused.
func TestDiagnosticsConfigRunAllowlistOnlyMembership(t *testing.T) {
	path := writeMinimalConfig(t, `[tools]
run_allowlist = ["git"]
run_allowlist_only = ["sh"]
diagnostics_command = ["git", "status"]
`)
	if _, err := Load(LoadOptions{ConfigPath: path}); err == nil {
		t.Fatal("Load accepted a diagnostics_command allowlisted only in run_allowlist while run_allowlist_only is set; want a validation error")
	} else if !strings.Contains(err.Error(), "diagnostics_command") {
		t.Fatalf("load error must name diagnostics_command, got: %v", err)
	}
}

// STE: when run_allowlist_only is set it IS the whole allowlist; an argv[0] it
// contains must pass validation.
func TestDiagnosticsConfigRunAllowlistOnlyPasses(t *testing.T) {
	path := writeMinimalConfig(t, `[tools]
run_allowlist_only = ["sh"]
diagnostics_command = ["sh", "-c", "echo hi"]
`)
	if _, err := Load(LoadOptions{ConfigPath: path}); err != nil {
		t.Fatalf("Load rejected a diagnostics_command on run_allowlist_only: %v", err)
	}
}
