package config

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// diagnostics_config_test.go is the config-layer contract for the
// get_diagnostics command surface (locked plan v2). The config layer must
// validate every configured command the way the tools layer's run_command
// gate does (internal/tools/run.go resolveAllowedCommand): argv[0] must be a
// bare name on the resolved run allowlist. Bad values are LOAD errors - a
// config whose diagnostics tool could never register must not load clean.
//
// V1 (implemented): [tools] diagnostics_command is a single argv; the tests
// below it exercise that surface.
//
// V2 (RED phase, this file): [tools] diagnostics_commands is a TOML map
// name->argv and the ONE surface; diagnostics_command is a deprecated alias
// that resolveToolsConfig folds into diagnostics_commands["default"] and then
// clears before Validate. The V2 tests below are RED against the CURRENT
// package: ToolsConfig has no DiagnosticsCommands field, resolveToolsConfig
// does not fold/clear the alias, and the lenient TOML decoder silently drops
// the unknown diagnostics_commands key, so Load succeeds where the V2
// contract says it must error. They compile because they drive the surface
// through the shared loading helper (writeMinimalConfig/Load) and read the
// resolved map through reflection (resolvedDiagnosticsCommands) - a direct
// res.Tools.DiagnosticsCommands reference cannot build until the field lands.

// resolvedDiagnosticsCommands reads the v2 DiagnosticsCommands map off the
// resolved ToolsConfig. The field does not exist yet (RED phase), so a direct
// field reference would not compile against the current package; reflection
// keeps this file buildable now and runs the real assertions the moment the
// field lands. A missing field fails the test with the same signal as the
// missing behavior: the v2 surface is absent.
func resolvedDiagnosticsCommands(t *testing.T, res *Resolved) map[string][]string {
	t.Helper()
	f := reflect.ValueOf(res.Tools).FieldByName("DiagnosticsCommands")
	if !f.IsValid() {
		t.Fatalf("resolved ToolsConfig has no DiagnosticsCommands map (v2 diagnostics_commands not implemented); got %+v", res.Tools)
	}
	cmds, ok := f.Interface().(map[string][]string)
	if !ok {
		t.Fatalf("resolved ToolsConfig.DiagnosticsCommands has type %s, want map[string][]string", f.Type())
	}
	return cmds
}

// === V1 surface: [tools] diagnostics_command (deprecated alias in V2) ===

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
	// "docker" is deliberately absent from tools.DefaultRunAllowlist (see
	// its doc comment), so it stays a genuine not-allowlisted case even
	// though the built-in default now makes run_command open by default.
	path := writeMinimalConfig(t, `[tools]
run_allowlist_only = ["git"]
diagnostics_command = ["docker", "--version"]
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

// === V2 surface: [tools] diagnostics_commands (the ONE surface) ===

// STE: the v1 diagnostics_command alias must fold into the map under the
// reserved name "default" and the deprecated field must be cleared before
// Validate sees the config, so the config layer validates exactly one
// surface. RED: the current package keeps DiagnosticsCommand populated and
// has no map, so the "cleared" assertion fails first.
func TestDiagnosticsConfigAliasFoldsIntoDefaultMapEntry(t *testing.T) {
	path := writeMinimalConfig(t, `[tools]
run_allowlist = ["sh"]
diagnostics_command = ["sh", "-c", "echo hi"]
`)
	res, err := Load(LoadOptions{ConfigPath: path})
	if err != nil {
		t.Fatalf("Load rejected a valid diagnostics_command alias: %v", err)
	}
	if len(res.Tools.DiagnosticsCommand) != 0 {
		t.Fatalf("resolved DiagnosticsCommand not cleared after alias fold, got %v", res.Tools.DiagnosticsCommand)
	}
	cmds := resolvedDiagnosticsCommands(t, res)
	got, ok := cmds["default"]
	if !ok {
		t.Fatalf("resolved DiagnosticsCommands has no \"default\" entry after alias fold, got %v", cmds)
	}
	want := []string{"sh", "-c", "echo hi"}
	if !slices.Equal(got, want) {
		t.Fatalf("resolved DiagnosticsCommands[\"default\"] = %v, want %v", got, want)
	}
}

// STE: the alias and the map are the same surface; setting both is ambiguous
// and must be a LOAD ERROR, not a silent precedence choice. RED: the current
// decoder drops the unknown diagnostics_commands key, so Load succeeds.
func TestDiagnosticsConfigAliasAndMapBothSetIsLoadError(t *testing.T) {
	path := writeMinimalConfig(t, `[tools]
run_allowlist = ["sh"]
diagnostics_command = ["sh", "-c", "true"]
diagnostics_commands = { lint = ["sh", "-c", "true"] }
`)
	if _, err := Load(LoadOptions{ConfigPath: path}); err == nil {
		t.Fatal("Load accepted both diagnostics_command and diagnostics_commands; want a load error")
	} else if !strings.Contains(err.Error(), "diagnostics_commands") {
		t.Fatalf("load error must name diagnostics_commands, got: %v", err)
	}
}

// STE: map keys are command names. Empty, whitespace-only, and case-folded
// duplicate keys cannot be selected by the model and must be LOAD ERRORS.
// "Lint" and "lint" are distinct TOML keys (TOML keys are case-sensitive), so
// the document parses; the case-folded collision is the config layer's
// rejection, not the parser's. RED: the map key is dropped today, so every
// subtest sees Load succeed where the contract says error.
func TestDiagnosticsConfigMapKeyValidation(t *testing.T) {
	cases := []struct {
		name string
		toml string
	}{
		{name: "empty key", toml: `[tools]
run_allowlist = ["sh"]
diagnostics_commands = { "" = ["sh", "-c", "true"] }
`},
		{name: "whitespace-only key", toml: `[tools]
run_allowlist = ["sh"]
diagnostics_commands = { "   " = ["sh", "-c", "true"] }
`},
		{name: "case-folded duplicate keys", toml: `[tools]
run_allowlist = ["sh"]
diagnostics_commands = { Lint = ["sh", "-c", "true"], lint = ["sh", "-c", "true"] }
`},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			path := writeMinimalConfig(t, tt.toml)
			if _, err := Load(LoadOptions{ConfigPath: path}); err == nil {
				t.Fatalf("Load accepted a diagnostics_commands map with invalid key %q; want a load error", tt.name)
			} else if !strings.Contains(err.Error(), "diagnostics_commands") {
				t.Fatalf("load error must name diagnostics_commands, got: %v", err)
			}
		})
	}
}

// STE: every map entry is validated like the run_command gate: non-empty
// argv, a bare argv[0] (no path separator), argv[0] on the EFFECTIVE run
// allowlist, and - new in V2 - argv[0] NOT on run_blocklist (the tools layer
// subtracts the blocklist in configuredRunAllowlist; the config layer mirrors
// that subtraction locally, like the existing allowlisted() mirror, so a
// command that could never register is a load error). RED: the map is dropped
// today, so every subtest sees Load succeed where the contract says error.
func TestDiagnosticsConfigMapEntryValidation(t *testing.T) {
	cases := []struct {
		name string
		toml string
	}{
		{name: "empty argv", toml: `[tools]
run_allowlist = ["sh"]
diagnostics_commands = { lint = [] }
`},
		{name: "path-shaped argv[0]", toml: `[tools]
run_allowlist = ["sh"]
diagnostics_commands = { lint = ["/bin/sh", "-c", "true"] }
`},
		{name: "argv[0] not on run_allowlist", toml: `[tools]
run_allowlist_only = ["git"]
diagnostics_commands = { lint = ["docker", "--version"] }
`},
		{name: "argv[0] on run_allowlist and run_blocklist", toml: `[tools]
run_allowlist = ["sh"]
run_blocklist = ["sh"]
diagnostics_commands = { lint = ["sh", "-c", "true"] }
`},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			path := writeMinimalConfig(t, tt.toml)
			if _, err := Load(LoadOptions{ConfigPath: path}); err == nil {
				t.Fatalf("Load accepted an invalid diagnostics_commands entry (%s); want a load error", tt.name)
			} else if !strings.Contains(err.Error(), "diagnostics_commands") {
				t.Fatalf("load error must name diagnostics_commands, got: %v", err)
			}
		})
	}
}

// STE: run_allowlist_only replaces run_allowlist entirely (resolveToolsConfig
// clears RunAllowlist when RunAllowlistOnly is set, before Validate runs), so
// map entries are checked against run_allowlist_only alone when it is set: a
// command allowlisted only in the non-authoritative run_allowlist must be
// refused. RED: the map is dropped today, so Load succeeds.
func TestDiagnosticsConfigMapRunAllowlistOnlyMembership(t *testing.T) {
	path := writeMinimalConfig(t, `[tools]
run_allowlist = ["git"]
run_allowlist_only = ["sh"]
diagnostics_commands = { lint = ["git", "status"] }
`)
	if _, err := Load(LoadOptions{ConfigPath: path}); err == nil {
		t.Fatal("Load accepted a diagnostics_commands entry allowlisted only in run_allowlist while run_allowlist_only is set; want a load error")
	} else if !strings.Contains(err.Error(), "diagnostics_commands") {
		t.Fatalf("load error must name diagnostics_commands, got: %v", err)
	}
}

// STE: when run_allowlist_only is set it IS the whole allowlist; an argv[0] it
// contains must pass validation and the resolved map must keep the entry.
func TestDiagnosticsConfigMapRunAllowlistOnlyPasses(t *testing.T) {
	path := writeMinimalConfig(t, `[tools]
run_allowlist_only = ["sh"]
diagnostics_commands = { lint = ["sh", "-c", "true"] }
`)
	res, err := Load(LoadOptions{ConfigPath: path})
	if err != nil {
		t.Fatalf("Load rejected a diagnostics_commands entry on run_allowlist_only: %v", err)
	}
	got, ok := resolvedDiagnosticsCommands(t, res)["lint"]
	if !ok || !slices.Equal(got, []string{"sh", "-c", "true"}) {
		t.Fatalf("resolved DiagnosticsCommands[\"lint\"] = %v (present %v), want [sh -c true]", got, ok)
	}
}

// STE: a valid multi-command map loads with every entry intact - a project can
// declare several named diagnostics commands and each must survive resolution.
// RED: the map key is dropped today, so the resolved-map assertion fails (no
// field).
func TestDiagnosticsConfigMapValidEntriesLoad(t *testing.T) {
	path := writeMinimalConfig(t, `[tools]
run_allowlist = ["sh"]
diagnostics_commands = { lint = ["sh", "-c", "true"], vet = ["sh", "-c", "true"] }
`)
	res, err := Load(LoadOptions{ConfigPath: path})
	if err != nil {
		t.Fatalf("Load rejected a valid diagnostics_commands map: %v", err)
	}
	cmds := resolvedDiagnosticsCommands(t, res)
	if len(cmds) != 2 {
		t.Fatalf("resolved DiagnosticsCommands has %d entries, want 2: %v", len(cmds), cmds)
	}
	for _, name := range []string{"lint", "vet"} {
		if got := cmds[name]; !slices.Equal(got, []string{"sh", "-c", "true"}) {
			t.Fatalf("resolved DiagnosticsCommands[%q] = %v, want [sh -c true]", name, got)
		}
	}
}

// STE: "default" is the reserved default command name and may be declared
// explicitly in the map when the deprecated alias is unset - the config layer
// must accept it like any other valid key.
func TestDiagnosticsConfigMapDefaultNameExplicit(t *testing.T) {
	path := writeMinimalConfig(t, `[tools]
run_allowlist = ["sh"]
diagnostics_commands = { default = ["sh", "-c", "true"] }
`)
	res, err := Load(LoadOptions{ConfigPath: path})
	if err != nil {
		t.Fatalf("Load rejected an explicit default entry in diagnostics_commands: %v", err)
	}
	got, ok := resolvedDiagnosticsCommands(t, res)["default"]
	if !ok || !slices.Equal(got, []string{"sh", "-c", "true"}) {
		t.Fatalf("resolved DiagnosticsCommands[\"default\"] = %v (present %v), want [sh -c true]", got, ok)
	}
}
