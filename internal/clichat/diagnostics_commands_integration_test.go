package clichat

// diagnostics_commands_integration_test.go is the INTEGRATION contract for the
// get_diagnostics command surface (locked plan v2): the faithful chain from a
// real .mivia/mivia.toml through the CLI's own wiring to a live registry
// Execute call. It proves the pieces the unit tests cover separately actually
// compose:
//
//	temp workspace -> write .mivia/mivia.toml declaring TWO diagnostics
//	commands (both argv[0]=sh, allowlisted) -> config.Load(ConfigPath,
//	WorkspaceRoot, AllowMissingConfig:true) -> tools.DefaultOptions mirrored
//	field-for-field from configureChatWorkspace (chat_workspace.go,
//	DiagnosticsCommands included) -> tools.NewDefaultRegistry -> assert
//	get_diagnostics is registered -> reg.Execute per command name.
//
// The unit halves of the same contract live in
// internal/config/diagnostics_config_test.go (load/validation) and
// internal/tools/diagnostics_registry_test.go (tool Execute). This file drives
// the whole chain with the real registry the chat session would install, so a
// wiring regression - a field the CLI forgets to map, a gate that silently
// drops the tool, an argv that cannot resolve from the config - fails here.
//
// The fixture is internal/tools/testdata/fake_diagnostics.sh. Tests run with
// the package directory as CWD, so the fixture path is
// ../tools/testdata/fake_diagnostics.sh made absolute. The fixture is POSIX,
// so the whole test skips on Windows (mirrors the requirePOSIX guard in
// internal/tools/diagnostics_registry_test.go and internal/hooks/exec_test.go).

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// requirePOSIXDiagnosticsIntegration mirrors the requirePOSIX guard used by
// the fixture-based tests in internal/tools: fake_diagnostics.sh is a POSIX
// shell script, so every test that executes it must skip on Windows.
func requirePOSIXDiagnosticsIntegration(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake_diagnostics.sh is a POSIX shell script")
	}
}

// diagnosticsIntegrationFixturePath returns the absolute fixture path. The
// test binary runs with the package directory as its working directory, so
// the ../tools/testdata-relative path resolves.
func diagnosticsIntegrationFixturePath(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("..", "tools", "testdata", "fake_diagnostics.sh"))
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

// writeDiagnosticsConfig writes .mivia/mivia.toml into dir. The document
// declares a provider + model (config.Load fails without a resolvable
// provider), allowlists sh (argv[0] of both commands), and carries the caller's
// diagnostics_commands TOML fragment. It returns the config path.
func writeDiagnosticsConfig(t *testing.T, dir, commandsTOML string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".mivia"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`[provider]
name = "deepseek"

[providers.deepseek]
models = [{ name = "deepseek-v4-flash", context_window_tokens = 128000 }]

[chat]
max_tokens = 8192

[tools]
run_allowlist = ["sh"]
%s`, commandsTOML)
	path := filepath.Join(dir, ".mivia", "mivia.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// diagnosticsChatOptions mirrors configureChatWorkspace's DefaultOptions
// mapping field-for-field (internal/cli/chat_workspace.go), so the registry
// this test drives is built exactly the way a real chat session builds it.
// DiagnosticsCommands is the field under test; the rest are mapped so a future
// wiring drift cannot hide behind a test that only fills the one field.
func diagnosticsChatOptions(t *testing.T, dir string, res *config.Resolved) tools.DefaultOptions {
	t.Helper()
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return tools.DefaultOptions{
		Workspace:                 ws,
		TavilyAPIKey:              res.TavilyAPIKey,
		RunAllowlist:              res.Tools.RunAllowlist,
		RunAllowlistOnly:          res.Tools.RunAllowlistOnly,
		RunBlocklist:              res.Tools.RunBlocklist,
		DisableTools:              res.Tools.DisableTools,
		EnvAllowlist:              res.Tools.EnvAllowlist,
		EnvAllowlistOnly:          res.Tools.EnvAllowlistOnly,
		EnvBlocklist:              res.Tools.EnvBlocklist,
		EnvAllowKeywordBlocklist:  res.Tools.EnvAllowKeywordBlocklist,
		RunTimeoutSec:             res.Tools.RunTimeoutSec,
		MaxReadBytes:              res.Tools.MaxReadBytes,
		MaxWriteKB:                res.Tools.MaxWriteKB,
		MaxOutputBytes:            res.Tools.MaxOutputBytes,
		MaxListDirEntries:         res.Tools.MaxListDirEntries,
		MaxToolResultBytes:        res.Tools.MaxToolResultBytes,
		MaxTavilyResponseBytes:    res.Tools.MaxTavilyResponseBytes,
		MaxFetchKB:                res.Tools.MaxFetchKB,
		MemoryBackstopBytes:       res.Tools.MemoryBackstopMB << 20,
		SecretPathPatterns:        res.Tools.SecretPathPatterns,
		SecretPathExceptions:      res.Tools.SecretPathExceptions,
		SearchIgnorePatterns:      res.Tools.SearchIgnorePatterns,
		MaxInspectRepositoryBytes: res.Tools.MaxInspectRepositoryBytes,
		DiagnosticsCommands:       res.Tools.DiagnosticsCommands,
	}
}

// diagnosticsRegistryFromConfig is the faithful chain under test: temp
// workspace -> .mivia/mivia.toml -> config.Load -> DefaultOptions (mirroring
// configureChatWorkspace) -> NewDefaultRegistry.
func diagnosticsRegistryFromConfig(t *testing.T, dir, commandsTOML string) *tools.Registry {
	t.Helper()
	cfgPath := writeDiagnosticsConfig(t, dir, commandsTOML)
	res, err := config.Load(config.LoadOptions{
		ConfigPath:         cfgPath,
		WorkspaceRoot:      dir,
		AllowMissingConfig: true,
	})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return tools.NewDefaultRegistry(diagnosticsChatOptions(t, dir, res))
}

// diagnosticsEnvelope is the minimal model-facing envelope shape the
// assertions read. It mirrors the tool's diagnosticsEnvelope (internal/tools/
// diagnostics.go) without importing the unexported type.
type diagnosticsEnvelope struct {
	Command     string `json:"command,omitempty"`
	CommandName string `json:"command_name,omitempty"`
	ExitCode    *int   `json:"exit_code,omitempty"`
	Rows        []struct {
		Severity string `json:"severity"`
		Message  string `json:"message"`
		File     string `json:"file,omitempty"`
		Line     int    `json:"line,omitempty"`
		Column   int    `json:"column,omitempty"`
		Raw      bool   `json:"raw,omitempty"`
	} `json:"rows"`
	Summary struct {
		Total    int `json:"total"`
		Errors   int `json:"errors"`
		Warnings int `json:"warnings"`
		Infos    int `json:"infos"`
		Raw      int `json:"raw"`
		Files    int `json:"files"`
	} `json:"summary"`
	Truncated bool   `json:"truncated,omitempty"`
	Error     string `json:"error,omitempty"`
}

// executeGetDiagnostics runs the registered get_diagnostics tool. On the
// success path it returns the parsed envelope; on an envelope-level failure it
// returns the parsed envelope too (the body stays valid JSON) plus the error.
func executeGetDiagnostics(t *testing.T, reg *tools.Registry, args string) (diagnosticsEnvelope, error) {
	t.Helper()
	out, err := reg.Execute(context.Background(), tools.GetDiagnosticsToolName, json.RawMessage(args))
	var env diagnosticsEnvelope
	if uerr := json.Unmarshal([]byte(out), &env); uerr != nil {
		t.Fatalf("get_diagnostics returned a non-JSON envelope %q: %v", out, uerr)
	}
	return env, err
}

// requireRowAt pins the parsed fields of the row for file, mirroring the tools
// package helper of the same name.
func requireRowAt(t *testing.T, rows []struct {
	Severity string `json:"severity"`
	Message  string `json:"message"`
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
	Column   int    `json:"column,omitempty"`
	Raw      bool   `json:"raw,omitempty"`
}, file, severity, message string, line, column int) {
	t.Helper()
	for _, row := range rows {
		if row.File != file {
			continue
		}
		if row.Severity != severity {
			t.Errorf("row %s: severity = %q, want %q", file, row.Severity, severity)
		}
		if row.Message != message {
			t.Errorf("row %s: message = %q, want %q", file, row.Message, message)
		}
		if row.Line != line {
			t.Errorf("row %s: line = %d, want %d", file, row.Line, line)
		}
		if row.Column != column {
			t.Errorf("row %s: column = %d, want %d", file, row.Column, column)
		}
		if row.Raw {
			t.Errorf("row %s: raw = true, want false", file)
		}
		return
	}
	t.Errorf("no row for file %q in %+v", file, rows)
}

// TestIntegrationDiagnosticsCommands is the integration contract (locked plan
// v2): a real .mivia/mivia.toml declaring two diagnostics commands reaches a
// live NewDefaultRegistry through the CLI's own DefaultOptions mapping, and
// Execute behaves per command name. Each scenario is a helper function so the
// test body stays within the function-LOC policy.
func TestIntegrationDiagnosticsCommands(t *testing.T) {
	requirePOSIXDiagnosticsIntegration(t)
	script := diagnosticsIntegrationFixturePath(t)
	assertDiagnosticsRegistered(t, script)
	assertDiagnosticsLintCommand(t, script)
	assertDiagnosticsDefaultCommand(t, script)
	assertDiagnosticsUnknownCommand(t, script)
	assertDiagnosticsNoDefaultAmbiguous(t, script)
}

// diagnosticsCommandsTOML renders the two-command map (default + lint) used by
// the first workspace.
func diagnosticsCommandsTOML(script string) string {
	return fmt.Sprintf(
		"diagnostics_commands = { default = [\"sh\", %q, \"--fail\"], lint = [\"sh\", %q, \"--format=json\"] }\n",
		script, script)
}

func assertDiagnosticsRegistered(t *testing.T, script string) {
	t.Helper()
	reg := diagnosticsRegistryFromConfig(t, t.TempDir(), diagnosticsCommandsTOML(script))
	if _, ok := reg.Get(tools.GetDiagnosticsToolName); !ok {
		t.Fatalf("get_diagnostics is NOT registered on the chat registry; have: %v", registryToolNames(reg))
	}
}

// assertDiagnosticsLintCommand: selecting "lint" runs the lint argv (JSON
// block rows, exit 0) and the envelope names the lint argv, never the
// default's.
func assertDiagnosticsLintCommand(t *testing.T, script string) {
	t.Helper()
	reg := diagnosticsRegistryFromConfig(t, t.TempDir(), diagnosticsCommandsTOML(script))
	env, err := executeGetDiagnostics(t, reg, `{"command":"lint"}`)
	if err != nil {
		t.Fatalf("lint command failed: %v", err)
	}
	if env.CommandName != "lint" {
		t.Errorf("command_name = %q, want %q", env.CommandName, "lint")
	}
	if !strings.Contains(env.Command, "fake_diagnostics.sh") || !strings.Contains(env.Command, "--format=json") {
		t.Errorf("command = %q, want it to name the lint argv (fixture + --format=json)", env.Command)
	}
	if strings.Contains(env.Command, "--fail") {
		t.Errorf("command = %q, must not leak the default argv (--fail) when lint is selected", env.Command)
	}
	if env.ExitCode == nil || *env.ExitCode != 0 {
		t.Errorf("exit_code = %v, want 0 for the lint command", env.ExitCode)
	}
	if len(env.Rows) != 3 {
		t.Fatalf("rows = %d, want 3 from the JSON block: %+v", len(env.Rows), env.Rows)
	}
	requireRowAt(t, env.Rows, "main.go", "error", "undefined: foo", 12, 5)
	requireRowAt(t, env.Rows, "vendor/helper.go", "warning", "unused variable bar", 3, 2)
	requireRowAt(t, env.Rows, "src/extra.go", "info", "third finding", 9, 0)
	if env.Summary.Total != 3 || env.Summary.Errors != 1 || env.Summary.Warnings != 1 || env.Summary.Infos != 1 {
		t.Errorf("summary = %+v, want 3 rows (1 error, 1 warning, 1 info)", env.Summary)
	}
}

// assertDiagnosticsDefaultCommand: omitting the command runs the default
// (line mode, exit 3) and the envelope names the default argv.
func assertDiagnosticsDefaultCommand(t *testing.T, script string) {
	t.Helper()
	reg := diagnosticsRegistryFromConfig(t, t.TempDir(), diagnosticsCommandsTOML(script))
	env, err := executeGetDiagnostics(t, reg, `{}`)
	if err != nil {
		t.Fatalf("default command failed: %v", err)
	}
	if env.CommandName != "default" {
		t.Errorf("command_name = %q, want %q", env.CommandName, "default")
	}
	if !strings.Contains(env.Command, "--fail") {
		t.Errorf("command = %q, want it to name the default argv (--fail)", env.Command)
	}
	// The default exits non-zero but still returns its rows.
	if env.ExitCode == nil || *env.ExitCode != 3 {
		t.Errorf("exit_code = %v, want 3 (the fixture's --fail exit)", env.ExitCode)
	}
	if len(env.Rows) != 3 {
		t.Fatalf("rows = %d, want 3 from line mode even on a non-zero exit: %+v", len(env.Rows), env.Rows)
	}
	requireRowAt(t, env.Rows, "main.go", "error", "undefined: foo", 12, 5)
	requireRowAt(t, env.Rows, "vendor/helper.go", "warning", "unused variable bar", 3, 2)
	rawFound := false
	for _, row := range env.Rows {
		if row.Raw && strings.Contains(row.Message, "raw noise line") {
			rawFound = true
			break
		}
	}
	if !rawFound {
		t.Errorf("no raw row echoing the noise line in %+v", env.Rows)
	}
}

// assertDiagnosticsUnknownCommand: an unknown name fails in the bounded
// envelope, never as a Go error.
func assertDiagnosticsUnknownCommand(t *testing.T, script string) {
	t.Helper()
	reg := diagnosticsRegistryFromConfig(t, t.TempDir(), diagnosticsCommandsTOML(script))
	env, err := executeGetDiagnostics(t, reg, `{"command":"bogus"}`)
	if err == nil {
		t.Fatal("an unknown command name must fail")
	}
	if !strings.Contains(env.Error, "unknown diagnostics command") {
		t.Errorf("envelope error = %q, want it to mention the unknown command", env.Error)
	}
}

// assertDiagnosticsNoDefaultAmbiguous: a SECOND temp workspace whose map has
// no "default" and two entries still registers (every selection is probed at
// Execute time), but an omitted command is ambiguous and must refuse in the
// envelope.
func assertDiagnosticsNoDefaultAmbiguous(t *testing.T, script string) {
	t.Helper()
	secondTOML := fmt.Sprintf(
		"diagnostics_commands = { lint = [\"sh\", %q, \"--format=json\"], vet = [\"sh\", %q, \"--fail\"] }\n",
		script, script)
	reg := diagnosticsRegistryFromConfig(t, t.TempDir(), secondTOML)
	if _, ok := reg.Get(tools.GetDiagnosticsToolName); !ok {
		t.Fatalf("get_diagnostics is NOT registered with a default-less two-entry map; have: %v", registryToolNames(reg))
	}
	env, err := executeGetDiagnostics(t, reg, `{}`)
	if err == nil {
		t.Fatal("an omitted command with no default and two entries must fail")
	}
	if !strings.Contains(env.Error, "multiple diagnostics commands") {
		t.Errorf("envelope error = %q, want it to mention 'multiple diagnostics commands'", env.Error)
	}
}
