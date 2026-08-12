package tools

// diagnostics_registry_test.go is the RED-phase contract test for the
// get_diagnostics tool surface (locked plan v2, task t2). It drives the tool
// through a registry built from NewRegistry() plus Register. It does NOT use
// the default registry, because default registration of get_diagnostics is a
// later task.
//
// The t1 skeleton implements Execute as a stub that returns "not implemented".
// Every assertion below fails until the implementation lands. That is the
// intended RED state.
//
// The fixture testdata/fake_diagnostics.sh is a real argv target. It is a
// POSIX script that emits a gcc-style line, a JSON block under --format=json,
// a raw-noise line, a non-zero exit path (--fail), a sleep path (--sleep=N),
// and a line whose file path carries a credential-like token (--redact).
// Fixture-based tests skip on Windows. This mirrors the requirePOSIX guard in
// internal/hooks/exec_test.go.
//
// Trust and security framing (locked plan v2 item 11): the command runs under
// the same program allowlist as run_command. A non-allowlisted argv[0] must
// produce run_command's refusal text. maxBytes bounds the capture; an
// over-budget capture must refuse with an error that names the bound, not emit
// truncated JSON. A timeout must surface as an error envelope. Whole-capture
// redaction with a configured policy must keep credential tokens out of the
// model-facing envelope.

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// requirePOSIXDiagnostics mirrors hooks/exec_test.go's requirePOSIX. The
// fixture is a POSIX shell script, so every test that executes it must skip on
// Windows.
func requirePOSIXDiagnostics(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake_diagnostics.sh is a POSIX shell script")
	}
}

// diagnosticsFixturePath returns the absolute path of the fixture. The test
// binary runs with the package directory as its working directory, so the
// testdata-relative path resolves.
func diagnosticsFixturePath(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("testdata", "fake_diagnostics.sh"))
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

// diagnosticsRegistry builds a registry that holds exactly one
// get_diagnostics tool. It does not touch the default registry.
func diagnosticsRegistry(t *testing.T, tool *getDiagnosticsTool) *Registry {
	t.Helper()
	reg := NewRegistry()
	reg.Register(tool)
	return reg
}

// diagnosticsResult is the outcome of one get_diagnostics call.
type diagnosticsResult struct {
	env     diagnosticsEnvelope
	out     string
	failure string
}

// runGetDiagnostics executes the registered get_diagnostics tool. It returns
// the envelope, the raw output, and the failure text. The failure text comes
// from the error return or from the envelope Error field. The envelope is the
// model-facing JSON shape, so a success body must parse as JSON.
func runGetDiagnostics(t *testing.T, reg *Registry, args json.RawMessage) diagnosticsResult {
	t.Helper()
	out, err := reg.Execute(context.Background(), GetDiagnosticsToolName, args)
	if err != nil {
		return diagnosticsResult{out: out, failure: err.Error()}
	}
	var env diagnosticsEnvelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("get_diagnostics returned a non-JSON envelope %q: %v", out, err)
	}
	return diagnosticsResult{env: env, out: out}
}

// diagnosticsEnvelopeNamed is the v2 envelope shape: diagnosticsEnvelope plus
// command_name, which names the selected commands entry. diagnostics.go does
// not carry command_name yet (it lands with v2t2), so the RED tests unmarshal
// the raw output into this extended shape instead of referencing a field that
// does not exist.
type diagnosticsEnvelopeNamed struct {
	diagnosticsEnvelope
	CommandName string `json:"command_name,omitempty"`
}

// namedEnvelopeOf unmarshals the raw tool output into the v2 envelope shape.
// It is used on the success path, where runGetDiagnostics already proved the
// body is valid JSON.
func namedEnvelopeOf(t *testing.T, res diagnosticsResult) diagnosticsEnvelopeNamed {
	t.Helper()
	var env diagnosticsEnvelopeNamed
	if err := json.Unmarshal([]byte(res.out), &env); err != nil {
		t.Fatalf("get_diagnostics returned a non-JSON envelope %q: %v", res.out, err)
	}
	return env
}

// requireRowAt pins the parsed fields of the row for file. It fails when no
// row carries that file.
func requireRowAt(t *testing.T, rows []diagnosticsRow, file, severity, message string, line, column int) {
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

// requireRawRow pins that one raw row echoes the noise line. Raw rows always
// carry severity info.
func requireRawRow(t *testing.T, rows []diagnosticsRow, message string) {
	t.Helper()
	for _, row := range rows {
		if row.Raw && strings.Contains(row.Message, message) {
			if row.Severity != "info" {
				t.Errorf("raw row severity = %q, want info", row.Severity)
			}
			return
		}
	}
	t.Errorf("no raw row containing %q in %+v", message, rows)
}

// TestGetDiagnosticsExecuteRunsRealArgv covers item (a): Execute runs a real
// argv through the fixture and returns parsed rows plus exit_code. The three
// subtests cover line mode, JSON mode, and the non-zero exit path.
func TestGetDiagnosticsExecuteRunsRealArgv(t *testing.T) {
	requirePOSIXDiagnostics(t)
	ws := setupTestWSRun(t)
	script := diagnosticsFixturePath(t)

	newTool := func(argv ...string) *Registry {
		return diagnosticsRegistry(t, &getDiagnosticsTool{
			ws:         ws,
			allowlist:  []string{"sh"},
			argv:       argv,
			timeoutSec: 30,
		})
	}

	t.Run("line_mode_parses_gcc_and_raw_rows", func(t *testing.T) {
		reg := newTool("sh", script)
		res := runGetDiagnostics(t, reg, json.RawMessage(`{}`))
		if res.failure != "" {
			t.Fatalf("unexpected failure: %s", res.failure)
		}
		if res.env.Version != diagnosticsEnvelopeVersion {
			t.Errorf("version = %d, want %d", res.env.Version, diagnosticsEnvelopeVersion)
		}
		if res.env.ExitCode == nil {
			t.Fatal("exit_code omitted for a completed run")
		}
		if *res.env.ExitCode != 0 {
			t.Errorf("exit_code = %d, want 0", *res.env.ExitCode)
		}
		if !strings.Contains(res.env.Command, "fake_diagnostics.sh") {
			t.Errorf("command = %q, want it to name the fixture", res.env.Command)
		}
		if res.env.Truncated {
			t.Error("truncated = true on an untruncated run")
		}
		if len(res.env.Rows) != 3 {
			t.Fatalf("rows = %d, want 3 (gcc + warning + raw): %+v", len(res.env.Rows), res.env.Rows)
		}
		requireRowAt(t, res.env.Rows, "main.go", "error", "undefined: foo", 12, 5)
		requireRowAt(t, res.env.Rows, "vendor/helper.go", "warning", "unused variable bar", 3, 2)
		requireRawRow(t, res.env.Rows, "raw noise line")
		wantSummary(t, diagnosticsOutput{Rows: res.env.Rows, Summary: res.env.Summary}, 1, 1, 0, 1, 2)
	})

	t.Run("json_mode_parses_diagnostics_block", func(t *testing.T) {
		reg := newTool("sh", script, "--format=json")
		res := runGetDiagnostics(t, reg, json.RawMessage(`{}`))
		if res.failure != "" {
			t.Fatalf("unexpected failure: %s", res.failure)
		}
		if res.env.ExitCode == nil || *res.env.ExitCode != 0 {
			t.Errorf("exit_code = %v, want 0", res.env.ExitCode)
		}
		if len(res.env.Rows) != 3 {
			t.Fatalf("rows = %d, want 3 from the JSON block: %+v", len(res.env.Rows), res.env.Rows)
		}
		requireRowAt(t, res.env.Rows, "main.go", "error", "undefined: foo", 12, 5)
		requireRowAt(t, res.env.Rows, "vendor/helper.go", "warning", "unused variable bar", 3, 2)
		requireRowAt(t, res.env.Rows, "src/extra.go", "info", "third finding", 9, 0)
		wantSummary(t, diagnosticsOutput{Rows: res.env.Rows, Summary: res.env.Summary}, 1, 1, 1, 0, 3)
	})

	t.Run("non_zero_exit_still_returns_rows", func(t *testing.T) {
		reg := newTool("sh", script, "--fail")
		res := runGetDiagnostics(t, reg, json.RawMessage(`{}`))
		if res.failure != "" {
			t.Fatalf("unexpected failure: %s", res.failure)
		}
		if res.env.ExitCode == nil || *res.env.ExitCode != 3 {
			t.Errorf("exit_code = %v, want 3", res.env.ExitCode)
		}
		if len(res.env.Rows) != 3 {
			t.Errorf("rows = %d, want 3 even on a non-zero exit", len(res.env.Rows))
		}
	})
}

// TestGetDiagnosticsRefusesNonAllowlistedArgv covers item (b): Execute refuses
// when argv[0] is not on the allowlist. The refusal must reuse the run_command
// error text, because get_diagnostics adds no execution authority beyond
// run_command.
func TestGetDiagnosticsRefusesNonAllowlistedArgv(t *testing.T) {
	ws := setupTestWSRun(t)
	reg := diagnosticsRegistry(t, &getDiagnosticsTool{
		ws:         ws,
		allowlist:  []string{"sh"},
		argv:       []string{"sudo", "echo", "hi"},
		timeoutSec: 30,
	})
	res := runGetDiagnostics(t, reg, json.RawMessage(`{}`))
	if res.failure == "" {
		t.Fatal("expected refusal for a non-allowlisted argv[0]")
	}
	if !strings.Contains(res.failure, "not allowlisted") {
		t.Errorf("refusal must reuse the run_command error text, got %q", res.failure)
	}
	if !strings.Contains(res.failure, "sudo") {
		t.Errorf("refusal must name the offending program, got %q", res.failure)
	}
}

// TestGetDiagnosticsOverBudgetRefusesEnvelope covers item (c): an over-budget
// capture must produce a refusal whose error names the maxBytes bound. It must
// not emit truncated JSON; runGetDiagnostics already requires valid JSON on
// the success path.
func TestGetDiagnosticsOverBudgetRefusesEnvelope(t *testing.T) {
	requirePOSIXDiagnostics(t)
	ws := setupTestWSRun(t)
	script := diagnosticsFixturePath(t)
	const bound = 128
	reg := diagnosticsRegistry(t, &getDiagnosticsTool{
		ws:         ws,
		allowlist:  []string{"sh"},
		argv:       []string{"sh", script, "--format=json"},
		timeoutSec: 30,
		maxBytes:   bound,
	})
	res := runGetDiagnostics(t, reg, json.RawMessage(`{}`))
	if res.failure == "" {
		t.Fatal("expected refusal when the capture exceeds maxBytes")
	}
	if !strings.Contains(res.failure, strconv.Itoa(bound)) {
		t.Errorf("refusal must name the bound %d, got %q", bound, res.failure)
	}
}

// TestGetDiagnosticsTimeoutReturnsErrorEnvelope covers item (d): a command
// that outlives timeoutSec must surface as an error envelope that says the run
// timed out.
func TestGetDiagnosticsTimeoutReturnsErrorEnvelope(t *testing.T) {
	requirePOSIXDiagnostics(t)
	ws := setupTestWSRun(t)
	script := diagnosticsFixturePath(t)
	reg := diagnosticsRegistry(t, &getDiagnosticsTool{
		ws:         ws,
		allowlist:  []string{"sh"},
		argv:       []string{"sh", script, "--sleep=5"},
		timeoutSec: 1,
	})
	res := runGetDiagnostics(t, reg, json.RawMessage(`{}`))
	if res.failure == "" {
		t.Fatal("expected an error envelope when the command times out")
	}
	lower := strings.ToLower(res.failure)
	if !strings.Contains(lower, "timeout") &&
		!strings.Contains(lower, "timed out") &&
		!strings.Contains(lower, "deadline") {
		t.Errorf("timeout failure must say so, got %q", res.failure)
	}
}

// TestGetDiagnosticsRedactsCredentialToken covers item (e): a credential token
// inside a parsed path field must be redacted in the output. Redaction is
// configuration, so the test installs a process-wide policy and mirrors the
// run_command redaction tests.
func TestGetDiagnosticsRedactsCredentialToken(t *testing.T) {
	requirePOSIXDiagnostics(t)
	useRedactionPolicy(t, []string{credentialPattern})
	ws := setupTestWSRun(t)
	script := diagnosticsFixturePath(t)
	// Assembled so the literal credential never appears in this file.
	token := "sk-" + "ant-fixture-redact-token-1234567890"
	reg := diagnosticsRegistry(t, &getDiagnosticsTool{
		ws:         ws,
		allowlist:  []string{"sh"},
		argv:       []string{"sh", script, "--redact"},
		timeoutSec: 30,
	})
	res := runGetDiagnostics(t, reg, json.RawMessage(`{}`))
	if res.failure != "" {
		t.Fatalf("unexpected failure: %s", res.failure)
	}
	if strings.Contains(res.out, token) {
		t.Errorf("credential token leaked in get_diagnostics output: %q", res.out)
	}
	if !strings.Contains(res.out, "[redacted]") {
		t.Errorf("expected the redaction placeholder in output, got %q", res.out)
	}
	redacted := false
	for _, row := range res.env.Rows {
		if strings.Contains(row.File, "[redacted]") {
			redacted = true
			break
		}
	}
	if !redacted {
		t.Errorf("expected a parsed row with a redacted file field, rows=%+v", res.env.Rows)
	}
}

// TestGetDiagnosticsMaxRowsDropsRows covers item (f): max_rows caps the
// envelope rows and sets truncated=true. The summary must describe the rows
// actually returned.
func TestGetDiagnosticsMaxRowsDropsRows(t *testing.T) {
	requirePOSIXDiagnostics(t)
	ws := setupTestWSRun(t)
	script := diagnosticsFixturePath(t)
	reg := diagnosticsRegistry(t, &getDiagnosticsTool{
		ws:         ws,
		allowlist:  []string{"sh"},
		argv:       []string{"sh", script, "--format=json"},
		timeoutSec: 30,
	})
	res := runGetDiagnostics(t, reg, json.RawMessage(`{"max_rows":1}`))
	if res.failure != "" {
		t.Fatalf("unexpected failure: %s", res.failure)
	}
	if !res.env.Truncated {
		t.Error("truncated = false, want true when max_rows drops rows")
	}
	if len(res.env.Rows) != 1 {
		t.Errorf("rows = %d, want 1 after max_rows=1", len(res.env.Rows))
	}
	if res.env.Summary.Total != 1 {
		t.Errorf("summary.total = %d, want 1 (summary describes the returned rows)", res.env.Summary.Total)
	}
}

// TestGetDiagnosticsCapability covers item (g): the tool schedules as
// ExecutionExternal and declares MaxResultBytes 0, because the result budget
// is a content budget declared through ResultBudgetBytes, not a wire
// truncation bound.
func TestGetDiagnosticsCapability(t *testing.T) {
	tool := &getDiagnosticsTool{timeoutSec: 30}
	reg := diagnosticsRegistry(t, tool)
	cap := reg.Capability(GetDiagnosticsToolName, json.RawMessage(`{}`))
	if cap.Class != ExecutionExternal {
		t.Errorf("capability class = %v, want ExecutionExternal", cap.Class)
	}
	if cap.MaxResultBytes != 0 {
		t.Errorf("capability MaxResultBytes = %d, want 0", cap.MaxResultBytes)
	}
	if cap.Timeout != 30*time.Second {
		t.Errorf("capability timeout = %s, want 30s", cap.Timeout)
	}
}

// TestGetDiagnosticsFilterEnvMatchesRunCommand pins review-gate rev2 finding
// 2: the get_diagnostics tool and run_command share ONE environment filter
// implementation (filterEnvFor). Given the same policy fields - including the
// keyword block - the two tools must produce byte-identical environments, so
// a child process can never see more through get_diagnostics than it could
// through run_command.
func TestGetDiagnosticsFilterEnvMatchesRunCommand(t *testing.T) {
	env := []string{
		"PATH=/usr/bin:/bin",
		"GIT_DIR=/repo/.git",
		"GIT_TOKEN=secret",
		"GIT_AUTHOR_NAME=x",
		"HOME=/home/u",
	}
	exact := map[string]bool{"PATH": true, "HOME": true}
	prefix := []string{"GIT_"}
	blockedExact := map[string]bool{"GIT_DIR": true}
	keywordBlock := []string{"TOKEN"}

	diag := &getDiagnosticsTool{
		envExact: exact, envPrefix: prefix, envBlockedExact: blockedExact, envKeywordBlock: keywordBlock,
	}
	run := &runCommandTool{
		envExact: exact, envPrefix: prefix, envBlockedExact: blockedExact, envKeywordBlock: keywordBlock,
	}
	got := diag.filterEnv(env)
	want := run.filterEnv(env)
	if len(got) != len(want) {
		t.Fatalf("get_diagnostics env = %v, run_command env = %v (shared filter drifted)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("env[%d] = %q, want %q (shared filter drifted)", i, got[i], want[i])
		}
	}
	// The keyword block must actually bite: GIT_TOKEN is admitted by the GIT_
	// prefix rule but must be dropped by the keyword filter, exactly like
	// run_command drops it.
	for _, e := range got {
		if strings.HasPrefix(e, "GIT_TOKEN=") {
			t.Fatalf("keyword-blocked variable leaked through: %q", e)
		}
	}
}

// TestGetDiagnosticsResolveExitCodePrecedence pins audit finding E1: a started
// process ALWAYS carries its real exit code, even when the parent context
// fires afterward; the *exec.ExitError is the process's own verdict and
// outranks the post-hoc context state. Timeout/cancel labels apply only to
// runs that never produced an exit.
func TestGetDiagnosticsResolveExitCodePrecedence(t *testing.T) {
	requirePOSIXDiagnostics(t)
	ws := setupTestWSRun(t)
	script := diagnosticsFixturePath(t)
	tool := &getDiagnosticsTool{
		ws:         ws,
		allowlist:  []string{"sh"},
		argv:       []string{"sh", script, "--fail"},
		timeoutSec: 30,
	}

	// Produce a REAL *exec.ExitError (exit 3) through the tool's own run
	// pipeline (the fixture's --fail path). The test never invokes a shell
	// directly, which the repo-wide TestNoShellPatterns guard scans for.
	bin, args, err := resolveAllowedCommand(tool.argv, tool.allowlist)
	if err != nil {
		t.Fatal(err)
	}
	callCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd, scope, err := tool.buildCommand(callCtx, bin, args)
	if err != nil {
		t.Fatal(err)
	}
	defer scope.cleanup()
	capture := tool.runCapture(cmd, callCtx, scope)
	if _, ok := capture.runErr.(*exec.ExitError); !ok {
		t.Fatalf("fixture --fail produced %v, want an *exec.ExitError", capture.runErr)
	}

	// The parent context fires AFTER the process already exited.
	ctx, cancel2 := context.WithCancel(context.Background())
	cancel2()
	code, msg, failed := tool.resolveExitCode(ctx, capture)
	if failed {
		t.Fatalf("an exited process reported a failure: %s", msg)
	}
	if code == nil || *code != 3 {
		t.Fatalf("exit code = %v, want 3 (process verdict must outrank the canceled ctx)", code)
	}

	// A clean exit stays exit 0 even under a canceled ctx.
	code, _, failed = tool.resolveExitCode(ctx, runCapture{runErr: nil})
	if failed || code == nil || *code != 0 {
		t.Fatalf("clean exit under canceled ctx = code %v failed %v, want 0/false", code, failed)
	}

	// A genuine timeout (the ctx fired and the run error IS the ctx error).
	tctx, tcancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer tcancel()
	<-tctx.Done()
	_, msg, failed = tool.resolveExitCode(tctx, runCapture{runErr: tctx.Err()})
	if !failed || !strings.Contains(msg, "timeout") {
		t.Fatalf("timeout run = failed %v msg %q, want failed with timeout text", failed, msg)
	}

	// A genuine cancel (the run error IS the canceled ctx error).
	cctx, ccancel := context.WithCancel(context.Background())
	ccancel()
	_, msg, failed = tool.resolveExitCode(cctx, runCapture{runErr: cctx.Err()})
	if !failed || !strings.Contains(msg, "canceled") {
		t.Fatalf("canceled run = failed %v msg %q, want failed with canceled text", failed, msg)
	}
}

// TestGetDiagnosticsJSONModeWithStderr pins audit finding E2: a JSON-emitting
// command that also writes to stderr must keep its structured rows. The
// streams are parsed independently; the stderr line becomes a raw row instead
// of poisoning JSON detection.
func TestGetDiagnosticsJSONModeWithStderr(t *testing.T) {
	requirePOSIXDiagnostics(t)
	ws := setupTestWSRun(t)
	script := `printf '{"diagnostics":[{"file":"a.go","line":1,"severity":"error","message":"m"}]}'; printf 'progress to stderr\n' >&2`
	reg := diagnosticsRegistry(t, &getDiagnosticsTool{
		ws: ws, allowlist: []string{"sh"},
		argv: []string{"sh", "-c", script}, timeoutSec: 30,
	})
	res := runGetDiagnostics(t, reg, json.RawMessage(`{}`))
	if res.failure != "" {
		t.Fatalf("unexpected failure: %s", res.failure)
	}
	if len(res.env.Rows) != 2 {
		t.Fatalf("rows = %d, want 2 (1 structured + 1 raw stderr): %+v", len(res.env.Rows), res.env.Rows)
	}
	requireRowAt(t, res.env.Rows, "a.go", "error", "m", 1, 0)
	requireRawRow(t, res.env.Rows, "progress to stderr")
	wantSummary(t, diagnosticsOutput{Rows: res.env.Rows, Summary: res.env.Summary}, 1, 0, 0, 1, 1)
}

// TestGetDiagnosticsTimeoutNotMaskedByBudget pins audit finding E3: a run that
// floods output past the capture budget AND then hangs past the deadline must
// report the timeout, not the budget refusal - the run outcome is classified
// before the budget check.
func TestGetDiagnosticsTimeoutNotMaskedByBudget(t *testing.T) {
	requirePOSIXDiagnostics(t)
	ws := setupTestWSRun(t)
	script := diagnosticsFixturePath(t)
	reg := diagnosticsRegistry(t, &getDiagnosticsTool{
		ws: ws, allowlist: []string{"sh"},
		argv:       []string{"sh", script, "--flood", "--sleep=5"},
		timeoutSec: 1,
		maxBytes:   512,
	})
	res := runGetDiagnostics(t, reg, json.RawMessage(`{}`))
	if res.failure == "" {
		t.Fatal("a hung, flooding command must fail")
	}
	if !strings.Contains(res.failure, "timeout") && !strings.Contains(res.failure, "timed out") {
		t.Fatalf("failure = %q, want the timeout to be reported (not masked by the budget refusal)", res.failure)
	}
}

// TestGetDiagnosticsJSONModeRedacts pins audit finding P5: in JSON mode the
// redact fixture element lives INSIDE the JSON array, so the capture stays one
// valid JSON document. The credential token inside a parsed file field is
// redacted while the JSON rows stay structured.
func TestGetDiagnosticsJSONModeRedacts(t *testing.T) {
	requirePOSIXDiagnostics(t)
	useRedactionPolicy(t, []string{credentialPattern})
	ws := setupTestWSRun(t)
	script := diagnosticsFixturePath(t)
	token := "sk-" + "ant-fixture-redact-token-1234567890"
	reg := diagnosticsRegistry(t, &getDiagnosticsTool{
		ws: ws, allowlist: []string{"sh"},
		argv: []string{"sh", script, "--format=json", "--redact"}, timeoutSec: 30,
	})
	res := runGetDiagnostics(t, reg, json.RawMessage(`{}`))
	if res.failure != "" {
		t.Fatalf("unexpected failure: %s", res.failure)
	}
	if len(res.env.Rows) != 4 {
		t.Fatalf("rows = %d, want 4 from the JSON block: %+v", len(res.env.Rows), res.env.Rows)
	}
	requireRowAt(t, res.env.Rows, "main.go", "error", "undefined: foo", 12, 5)
	requireRowAt(t, res.env.Rows, "vendor/helper.go", "warning", "unused variable bar", 3, 2)
	if strings.Contains(res.out, token) {
		t.Fatal("credential token leaked into the model-facing envelope")
	}
	// The redacted row keeps its structure; its file no longer contains the token.
	found := false
	for _, row := range res.env.Rows {
		if strings.Contains(row.File, "auth.go") && strings.Contains(row.Message, "boom") {
			found = true
			if strings.Contains(row.File, token) {
				t.Fatalf("credential token leaked into a parsed file field: %q", row.File)
			}
		}
	}
	if !found {
		t.Fatalf("no redacted auth.go row in %+v", res.env.Rows)
	}
}

// ---------------------------------------------------------------------------
// v2t1 RED contract tests: command selection (locked plan v2). The production
// skeleton in get_diagnostics.go now carries the commands map + defaultName
// fields and parses the "command" argument, but resolution is still the
// not-implemented stub, so every assertion below fails until v2t2 lands real
// selection. The tests construct the tool with the CURRENT struct fields plus
// the new commands/defaultName fields, so they compile against the skeleton
// and fail on assertions - not on compilation.
// ---------------------------------------------------------------------------

// TestGetDiagnosticsCommandSelectionRunsNamedCommand covers v2t1 item (a): a
// tool configured with commands {default, lint} and defaultName "default"
// must select the argv by the "command" argument. Execute {"command":"lint"}
// must run the lint argv (JSON mode) and return rows from the JSON block; the
// envelope Command must name the lint argv and CommandName must be "lint".
func TestGetDiagnosticsCommandSelectionRunsNamedCommand(t *testing.T) {
	requirePOSIXDiagnostics(t)
	ws := setupTestWSRun(t)
	script := diagnosticsFixturePath(t)
	reg := diagnosticsRegistry(t, &getDiagnosticsTool{
		ws:        ws,
		allowlist: []string{"sh"},
		commands: map[string][]string{
			"default": []string{"sh", script, "--fail"},
			"lint":    []string{"sh", script, "--format=json"},
		},
		defaultName: "default",
		timeoutSec:  30,
	})
	res := runGetDiagnostics(t, reg, json.RawMessage(`{"command":"lint"}`))
	if res.failure != "" {
		t.Fatalf("unexpected failure: %s", res.failure)
	}
	env := namedEnvelopeOf(t, res)
	if env.CommandName != "lint" {
		t.Errorf("command_name = %q, want %q", env.CommandName, "lint")
	}
	if !strings.Contains(env.Command, "fake_diagnostics.sh") || !strings.Contains(env.Command, "--format=json") {
		t.Errorf("command = %q, want it to name the lint argv (fixture + --format=json)", env.Command)
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
}

// TestGetDiagnosticsCommandOmittedRunsDefault covers v2t1 item (b): omitting
// the "command" argument must run the defaultName entry. The envelope
// CommandName must be "default" and Command must name the default argv (the
// default exits non-zero but still returns its rows).
func TestGetDiagnosticsCommandOmittedRunsDefault(t *testing.T) {
	requirePOSIXDiagnostics(t)
	ws := setupTestWSRun(t)
	script := diagnosticsFixturePath(t)
	reg := diagnosticsRegistry(t, &getDiagnosticsTool{
		ws:        ws,
		allowlist: []string{"sh"},
		commands: map[string][]string{
			"default": []string{"sh", script, "--fail"},
			"lint":    []string{"sh", script, "--format=json"},
		},
		defaultName: "default",
		timeoutSec:  30,
	})
	res := runGetDiagnostics(t, reg, json.RawMessage(`{}`))
	if res.failure != "" {
		t.Fatalf("unexpected failure: %s", res.failure)
	}
	env := namedEnvelopeOf(t, res)
	if env.CommandName != "default" {
		t.Errorf("command_name = %q, want %q", env.CommandName, "default")
	}
	if !strings.Contains(env.Command, "--fail") {
		t.Errorf("command = %q, want it to name the default argv (--fail)", env.Command)
	}
	if env.ExitCode == nil || *env.ExitCode != 3 {
		t.Errorf("exit_code = %v, want 3 (the default command exits non-zero but still returns rows)", env.ExitCode)
	}
	if len(env.Rows) != 3 {
		t.Errorf("rows = %d, want 3 even on the default's non-zero exit", len(env.Rows))
	}
}

// TestGetDiagnosticsCommandUnknownFails covers v2t1 item (c): an unknown
// command name must produce a failureEnvelope-shaped refusal whose Error
// contains 'unknown diagnostics command'. The refusal body must stay valid
// JSON.
func TestGetDiagnosticsCommandUnknownFails(t *testing.T) {
	ws := setupTestWSRun(t)
	script := diagnosticsFixturePath(t)
	reg := diagnosticsRegistry(t, &getDiagnosticsTool{
		ws:        ws,
		allowlist: []string{"sh"},
		commands: map[string][]string{
			"default": []string{"sh", script, "--fail"},
		},
		defaultName: "default",
		timeoutSec:  30,
	})
	res := runGetDiagnostics(t, reg, json.RawMessage(`{"command":"bogus"}`))
	if res.failure == "" {
		t.Fatal("an unknown command name must fail")
	}
	var env struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(res.out), &env); err != nil {
		t.Fatalf("failure envelope is not valid JSON %q: %v", res.out, err)
	}
	if !strings.Contains(env.Error, "unknown diagnostics command") {
		t.Errorf("envelope error = %q, want it to contain 'unknown diagnostics command'", env.Error)
	}
}

// TestGetDiagnosticsNoDefaultMultipleCommandsFails covers v2t1 item (d): with
// no defaultName and more than one entry, an omitted command must produce a
// failureEnvelope-shaped refusal whose Error contains 'multiple diagnostics
// commands'. The refusal body must stay valid JSON.
func TestGetDiagnosticsNoDefaultMultipleCommandsFails(t *testing.T) {
	ws := setupTestWSRun(t)
	script := diagnosticsFixturePath(t)
	reg := diagnosticsRegistry(t, &getDiagnosticsTool{
		ws:        ws,
		allowlist: []string{"sh"},
		commands: map[string][]string{
			"lint": []string{"sh", script, "--format=json"},
			"test": []string{"sh", script, "--fail"},
		},
		timeoutSec: 30,
	})
	res := runGetDiagnostics(t, reg, json.RawMessage(`{}`))
	if res.failure == "" {
		t.Fatal("an omitted command with no default and multiple entries must fail")
	}
	var env struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(res.out), &env); err != nil {
		t.Fatalf("failure envelope is not valid JSON %q: %v", res.out, err)
	}
	if !strings.Contains(env.Error, "multiple diagnostics commands") {
		t.Errorf("envelope error = %q, want it to contain 'multiple diagnostics commands'", env.Error)
	}
}

// TestGetDiagnosticsEnvelopeReflectsSelectedCommand pins auditor F5 (v2t1 item
// (e)): the envelope Command/CommandName must reflect the SELECTED argv even
// when it differs from the default. Executing the lint command must surface
// the lint argv - never the default's.
func TestGetDiagnosticsEnvelopeReflectsSelectedCommand(t *testing.T) {
	requirePOSIXDiagnostics(t)
	ws := setupTestWSRun(t)
	script := diagnosticsFixturePath(t)
	reg := diagnosticsRegistry(t, &getDiagnosticsTool{
		ws:        ws,
		allowlist: []string{"sh"},
		commands: map[string][]string{
			"default": []string{"sh", script, "--fail"},
			"lint":    []string{"sh", script, "--format=json"},
		},
		defaultName: "default",
		timeoutSec:  30,
	})
	res := runGetDiagnostics(t, reg, json.RawMessage(`{"command":"lint"}`))
	if res.failure != "" {
		t.Fatalf("unexpected failure: %s", res.failure)
	}
	env := namedEnvelopeOf(t, res)
	if env.CommandName != "lint" {
		t.Errorf("command_name = %q, want %q (the selected command, not the default)", env.CommandName, "lint")
	}
	if !strings.Contains(env.Command, "--format=json") {
		t.Errorf("command = %q, want it to carry the selected lint argv", env.Command)
	}
	if strings.Contains(env.Command, "--fail") {
		t.Errorf("command = %q, must not leak the default argv (--fail) when lint is selected", env.Command)
	}
}
