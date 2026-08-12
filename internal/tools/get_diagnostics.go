package tools

// get_diagnostics.go is the tool-surface half of the get_diagnostics tool
// (locked plan v2 item 5). It owns the struct, the registry name, the
// description, the parameter schema, the scheduling metadata, and the Execute
// pipeline: resolve the configured argv against the run_command allowlist,
// run it under the same process guards and capture pattern as run_command,
// redact the whole capture before parsing, and compose a budget-bounded JSON
// envelope. The row and envelope shapes live in diagnostics.go (locked plan
// v2 items p1 and p3) and this file reuses them.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/redact"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// GetDiagnosticsToolName is the registry name of the diagnostics tool.
const GetDiagnosticsToolName = "get_diagnostics"

// getDiagnosticsTool runs a project diagnostics command and returns the
// findings as structured rows. The command runs under the same program
// allowlist and process guards as run_command. maxBytes bounds the result
// envelope. The row and envelope shapes come from diagnostics.go.
type getDiagnosticsTool struct {
	// ws is the workspace root the tool resolves paths against.
	ws *workspace.Root
	// allowlist is the program allowlist; the same gate applies to the
	// diagnostics command as to run_command.
	allowlist []string
	// argv is the command line the tool runs. argv[0] is resolved against
	// the allowlist at Execute time, exactly as run_command resolves it. It
	// is the legacy single-command surface; a tool configured with commands
	// selects its argv through the commands map instead.
	argv []string
	// commands maps a command name to its argv: the v2 selection surface
	// (locked plan v2). When set, Execute selects the argv by the "command"
	// argument, falling back to defaultName when the argument is omitted. A
	// nil commands map keeps the legacy argv path.
	commands map[string][]string
	// defaultName names the commands entry executed when the "command"
	// argument is omitted. It is consulted only when commands is set.
	defaultName string
	// timeoutSec is the tool-level timeout for the command.
	timeoutSec int
	// maxBytes bounds the result envelope. Zero means uncapped.
	maxBytes int
	// envExact, envPrefix, envBlockedExact, and envKeywordBlock mirror the
	// run_command environment filter policy and are applied to the command.
	envExact        map[string]bool
	envPrefix       []string
	envBlockedExact map[string]bool
	envKeywordBlock []string
	// secretPathExceptions and secretPathPatterns guard argv against
	// secret-like paths, as in run_command.
	secretPathExceptions []string
	secretPathPatterns   []string
}

// Name returns the registry name of the tool.
func (t *getDiagnosticsTool) Name() string { return GetDiagnosticsToolName }

// Description returns the model-facing summary of the tool. The text stays
// project- and language-generic: it names no host language and no product
// path (rule 60, enforced by generic_surface_test.go).
func (t *getDiagnosticsTool) Description() string {
	return "Run a project diagnostics command and return the findings as structured rows. " +
		"Use this tool when a build, test, or lint step fails and you need the error list. " +
		"The command runs under the same program allowlist as run_command. " +
		"Examples: [\"make\",\"test\"], [\"npm\",\"run\",\"lint\"], [\"pytest\"]. " +
		"Use max_rows to cap the number of findings in the result."
}

// Parameters returns the JSON schema for the tool arguments. command selects
// the entry from the configured commands map (v2 selection surface); max_rows
// is optional and, when set, caps the number of rows in the result envelope.
func (t *getDiagnosticsTool) Parameters() map[string]any {
	return schemaObject(map[string]any{
		"command": map[string]any{
			"type":        "string",
			"description": "Optional name of the diagnostics command to run. When omitted, the configured default command runs.",
		},
		"max_rows": map[string]any{
			"type":        "integer",
			"minimum":     float64(1),
			"description": "Optional maximum number of rows to return. When omitted, the tool returns all rows.",
		},
	}, nil)
}

// Capability declares the scheduling metadata for one invocation. The tool
// runs an external process, so the class is ExecutionExternal. The timeout
// comes from the configured timeoutSec. MaxResultBytes stays zero: the result
// budget is a content budget declared through ResultBudgetBytes, not a wire
// truncation bound.
func (t *getDiagnosticsTool) Capability(json.RawMessage) Capability {
	return Capability{
		Class:   ExecutionExternal,
		Timeout: time.Duration(t.timeoutSec) * time.Second,
	}
}

// ResultBudgetBytes declares the result envelope budget for dispatcher
// ceiling derivation (see tools.ResultBudgetTool). The value comes from the
// configured maxBytes.
func (t *getDiagnosticsTool) ResultBudgetBytes() int { return t.maxBytes }

// Execute runs the tool. Pipeline (locked plan v2 section 5): context check,
// decode max_rows and command, resolve the argv through the commands map
// (the "command" argument selects the entry, defaultName when omitted; a
// refusal is an envelope-level failure carried in the envelope Error field
// and returned as a Go error, never a child process), resolve the selected
// argv against the allowlist, screen argv for secret-like paths, run the
// command under the run_command process guards, redact the whole capture
// BEFORE parsing, parse into rows, apply max_rows with truncated=true, and
// marshal the envelope under the maxBytes budget.
//
// Exit-code semantics mirror run.go's exitStatus precedence: a started
// process always carries its real exit code (0 on a clean exit, the child's
// code otherwise - and its rows are still returned on a non-zero exit); a run
// that never produced an exit (Start/LookPath failure, timeout, cancel)
// omits exit_code and surfaces as an error envelope rendering run.go's
// exit=timeout/canceled/error status text.
//
// The returned body is always valid JSON: an over-budget envelope is refused
// with a bounded error envelope naming the bound, never tail-cut.
func (t *getDiagnosticsTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var in struct {
		MaxRows int    `json:"max_rows"`
		Command string `json:"command"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}

	// v2 command selection (locked plan v2): a command-configured tool
	// resolves its argv through the commands map by the "command" argument,
	// falling back to defaultName when omitted; the legacy single-argv
	// surface (argv) is unchanged for tools without commands.
	name, argv, errMsg := t.resolveDiagnosticsCommand(in.Command)
	if errMsg != "" {
		return t.failureEnvelope(errMsg)
	}

	bin, commandArgs, err := resolveAllowedCommand(argv, t.allowlist)
	if err != nil {
		return t.failureEnvelope(err.Error())
	}
	// Same policy as read_file/write/run_command: do not let an allowlisted
	// utility bypass secret-path blocks via argv. Fail closed before process
	// start.
	if secret := secretPathInArgv(commandArgs, t.secretPathExceptions, t.secretPathPatterns); secret != "" {
		_ = secret // operand intentionally not surfaced: revealing which secret
		// path was blocked confirms its existence to the model (mirrors run.go).
		const msg = "accessing secret-like path is blocked"
		return t.failureEnvelope(msg)
	}

	// Run under the run_command process guards; the outcome is classified
	// before any parsing (audit findings E1, E3).
	exitCode, capture, msg, failed := t.runDiagnosticsCommand(ctx, bin, commandArgs)
	if failed {
		return t.failureEnvelope(msg)
	}

	// Redact each stream BEFORE parsing so a credential hidden inside a
	// parsed file field can never reach a row (locked plan v2 item 11). The
	// streams stay separate: concatenating them would poison JSON detection
	// whenever stderr carries any byte (audit finding E2).
	redactedOut := redact.Text(capture.stdout)
	redactedErr := redact.Text(capture.stderr)
	parsed, err := t.parseRedactedCapture(redactedOut, redactedErr)
	if err != nil {
		return t.failureEnvelope(err.Error())
	}

	// max_rows caps the rows the envelope carries; the summary always
	// describes the rows actually returned (locked plan v2 item 6).
	rows := parsed.Rows
	maxRowTruncated := false
	if in.MaxRows > 0 && len(rows) > in.MaxRows {
		rows = rows[:in.MaxRows]
		maxRowTruncated = true
	}
	return t.composeEnvelope(name, argv, rows, exitCode, maxRowTruncated)
}

// runDiagnosticsCommand runs the resolved command under the run_command
// process guards and capture budget. It classifies the run outcome FIRST
// (audit findings E1, E3): a started process always carries its real exit
// code, even when the parent context fires later during redaction/parsing; a
// run that never produced an exit (start failure, timeout, cancel) is an
// envelope-level failure; an elided capture is refused. On success it returns
// the exit code and the capture (for redaction) with failed=false.
func (t *getDiagnosticsTool) runDiagnosticsCommand(ctx context.Context, bin string, commandArgs []string) (*int, runCapture, string, bool) {
	callCtx, cancel := t.callContext(ctx, 0)
	if cancel != nil {
		defer cancel()
	}
	cmd, scope, err := t.buildCommand(callCtx, bin, commandArgs)
	if err != nil {
		return nil, runCapture{}, err.Error(), true
	}
	defer scope.cleanup()

	capture := t.runCapture(cmd, callCtx, scope)

	exitCode, msg, failed := t.resolveExitCode(callCtx, capture)
	if failed {
		return nil, capture, msg, true
	}
	if capture.truncated {
		msg := fmt.Sprintf("get_diagnostics output exceeds capture budget %d", capture.limit)
		return nil, capture, msg, true
	}
	return exitCode, capture, "", false
}

// resolveDiagnosticsCommand selects the argv for the "command" argument on a
// command-configured tool (locked plan v2). It returns the entry name, its
// argv, and an errMsg when resolution fails. An omitted command resolves to
// defaultName; with no default and more than one entry that is an ambiguity
// error ('multiple diagnostics commands configured; specify which with
// command'); an unknown name is an error ('unknown diagnostics command: X').
// A nil commands map (the legacy single-argv surface) returns the tool argv
// with an empty name.
func (t *getDiagnosticsTool) resolveDiagnosticsCommand(command string) (string, []string, string) {
	if t.commands == nil {
		return "", t.argv, ""
	}
	name := command
	if name == "" {
		name = t.defaultName
	}
	if name == "" {
		if len(t.commands) > 1 {
			return "", nil, "multiple diagnostics commands configured; specify which with command"
		}
		// A sole configured command with no default is unambiguous: pick it.
		for candidate := range t.commands {
			name = candidate
		}
	}
	argv, ok := t.commands[name]
	if !ok {
		return "", nil, fmt.Sprintf("unknown diagnostics command: %s", name)
	}
	return name, argv, ""
}

// parseRedactedCapture parses the two redacted streams independently so a
// stderr byte can never poison JSON detection on stdout (audit finding E2),
// then merges their rows and derives the summary from the merged set.
func (t *getDiagnosticsTool) parseRedactedCapture(redactedOut, redactedErr string) (diagnosticsOutput, error) {
	outParsed, err := parseDiagnosticsOutput([]byte(redactedOut), t.workspaceRoot())
	if err != nil {
		return diagnosticsOutput{}, err
	}
	errParsed, err := parseDiagnosticsOutput([]byte(redactedErr), t.workspaceRoot())
	if err != nil {
		return diagnosticsOutput{}, err
	}
	return finalizeDiagnostics(append(outParsed.Rows, errParsed.Rows...)), nil
}

// resolveExitCode interprets the run outcome into the envelope's exit code.
// A started process that EXITED carries its real exit code, even non-zero,
// and even when the parent context fires later during redaction/parsing: the
// *exec.ExitError with Exited()==true is the process's own verdict and
// outranks the post-hoc context state (audit finding E1). A process killed by
// the context never exits (Exited()==false): that is a timeout or cancel, and
// the run.go status text wins over a budget refusal (audit finding E3). A run
// that never produced an exit (Start or LookPath failure) omits exit_code and
// renders run.go's exit=error style. The parsed rows are still returned on a
// non-zero exit.
func (t *getDiagnosticsTool) resolveExitCode(callCtx context.Context, capture runCapture) (*int, string, bool) {
	if ee, ok := capture.runErr.(*exec.ExitError); ok && ee.Exited() {
		ec := ee.ExitCode()
		return &ec, "", false
	}
	switch {
	case capture.runErr == nil:
		ec := 0
		return &ec, "", false
	case callCtx.Err() == context.DeadlineExceeded || callCtx.Err() == context.Canceled:
		// The command never produced an exit: omit exit_code and surface the
		// run.go status text (exit=timeout / exit=canceled).
		return nil, "get_diagnostics: " + exitStatus(callCtx, capture.runErr), true
	default:
		// The process never started (Start failure) or was killed outside the
		// context: omit exit_code and render run.go's exit=error style.
		return nil, "get_diagnostics: " + exitStatus(callCtx, capture.runErr), true
	}
}

// callContext derives the effective per-call timeout context, mirroring
// runCommandTool.callContext byte-for-byte. get_diagnostics has no per-call
// timeout argument, so Execute always passes requested=0 and the tool-level
// timeoutSec is the bound: clamped to a 24h absolute ceiling and never
// extending a parent deadline (when the parent is the tighter bound the
// parent context is handed through unchanged, so the cancel func is nil).
func (t *getDiagnosticsTool) callContext(ctx context.Context, requested int) (context.Context, context.CancelFunc) {
	const absoluteCeiling = 24 * time.Hour
	timeout := time.Duration(t.timeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 900 * time.Second
	}
	if requested > 0 {
		// Clamp BEFORE the multiply: a seconds value too large for
		// time.Duration wraps negative, and the wrapped value slips past the
		// `timeout > absoluteCeiling` clamp below.
		if int64(requested) > maxDurationSeconds {
			requested = int(maxDurationSeconds)
		}
		req := time.Duration(requested) * time.Second
		if _, ok := ctx.Deadline(); ok {
			timeout = req
		} else if req < timeout {
			timeout = req
		}
	}
	if timeout > absoluteCeiling {
		timeout = absoluteCeiling
	}
	if parentDeadline, ok := ctx.Deadline(); ok && time.Until(parentDeadline) < timeout {
		return ctx, nil
	}
	return context.WithTimeout(ctx, timeout)
}

// buildCommand assembles the *exec.Cmd with the run_command process scope,
// the workspace root as the working directory, and the filtered minimal env.
func (t *getDiagnosticsTool) buildCommand(callCtx context.Context, bin string, commandArgs []string) (*exec.Cmd, commandScope, error) {
	cmd := exec.CommandContext(callCtx, bin, commandArgs...)
	cmd.WaitDelay = 2 * time.Second
	scope, err := prepareCommand(cmd)
	if err != nil {
		return nil, commandScope{}, fmt.Errorf("prepare command process scope: %w", err)
	}
	cmd.Cancel = func() error { return scope.cancel(cmd) }
	if t.ws != nil {
		cmd.Dir = t.ws.Abs
	}
	cmd.Env = t.filterEnv(os.Environ())
	return cmd, scope, nil
}

// runCapture executes the prepared command against one shared capture budget
// across stdout+stderr, mirroring runCommandTool.runCapture. The budget is
// ResultBudgetBytes (maxBytes); an uncapped tool still gets the memory
// backstop so honest multi-MB capture cannot OOM.
func (t *getDiagnosticsTool) runCapture(cmd *exec.Cmd, callCtx context.Context, scope commandScope) runCapture {
	limit := t.ResultBudgetBytes()
	cap := newDualCapture(limit)
	if limit <= 0 {
		cap = newMemoryBoundDualCapture(defaultMemoryBackstopBytes)
		limit = cap.headQuota + cap.tailQuota
	}
	cmd.Stdout = cap.Stdout()
	cmd.Stderr = cap.Stderr()
	var runErr error
	if err := cmd.Start(); err != nil {
		runErr = err
	} else if err := scope.attach(cmd); err != nil {
		_ = scope.cancel(cmd)
		_ = waitCommand(cmd, callCtx, scope)
		runErr = err
	} else {
		runErr = waitCommand(cmd, callCtx, scope)
	}
	return runCapture{
		stdout:    cap.StdoutString(),
		stderr:    cap.StderrString(),
		truncated: cap.Truncated(),
		limit:     limit,
		runErr:    runErr,
	}
}

// filterEnv computes the minimal environment for the child process. It is
// the single shared implementation with run_command: both tools must never
// drift apart on what a child process may see (review gate rev2 finding 2).
func (t *getDiagnosticsTool) filterEnv(env []string) []string {
	return filterEnvFor(env, t.envExact, t.envPrefix, t.envBlockedExact, t.envKeywordBlock)
}

// workspaceRoot returns the workspace root used to relativize parsed file
// paths. An empty root (or nil workspace) leaves paths verbatim.
func (t *getDiagnosticsTool) workspaceRoot() string {
	if t.ws == nil {
		return ""
	}
	return t.ws.Abs
}

// composeEnvelope marshals the model-facing envelope under the maxBytes
// budget. name/argv are the resolved command identity: Command renders the
// argv redacted and shell-safe (FormatArgv) and CommandName carries the
// commands entry name ("" omits it on the legacy surface). Rows are the
// variable-length part: when the composed JSON exceeds the bound, the largest
// fitting row prefix is kept with truncated=true (budgetedJSON's binary
// search, as in nav_json.go). When even a zero-row envelope exceeds the
// bound, the tool refuses with a bounded error envelope naming the bound - it
// never tail-cuts and never returns invalid JSON.
func (t *getDiagnosticsTool) composeEnvelope(name string, argv []string, rows []diagnosticsRow, exitCode *int, maxRowTruncated bool) (string, error) {
	build := func(keep int, budgetTruncated bool) any {
		kept := rows
		if keep < len(kept) {
			kept = kept[:keep]
		}
		if kept == nil {
			kept = []diagnosticsRow{}
		}
		return diagnosticsEnvelope{
			Version:     diagnosticsEnvelopeVersion,
			Command:     redact.Text(FormatArgv(argv)),
			CommandName: name,
			ExitCode:    exitCode,
			Rows:        kept,
			Summary:     finalizeDiagnostics(kept).Summary,
			Truncated:   maxRowTruncated || budgetTruncated,
		}
	}
	if t.maxBytes > 0 {
		if zero, err := json.Marshal(build(0, true)); err == nil && len(zero) > t.maxBytes {
			// Even a zero-row envelope exceeds the bound: refuse outright.
			// The refusal names the bound and stays a bounded, valid envelope.
			msg := fmt.Sprintf("get_diagnostics result exceeds maxBytes %d", t.maxBytes)
			return t.failureEnvelope(msg)
		}
	}
	return budgetedJSON(t.maxBytes, len(rows), build,
		diagnosticsRefusalEnvelope("get_diagnostics result exceeds result budget")), nil
}

// failureEnvelope composes the bounded error envelope for an envelope-level
// failure (resolve/start/timeout/cancel/budget refusal). The model-facing
// body carries the error in the Error field and omits exit_code; the same
// message is returned as the Go error so the dispatcher surfaces it too.
func (t *getDiagnosticsTool) failureEnvelope(msg string) (string, error) {
	return diagnosticsRefusalEnvelope(msg), errors.New(msg)
}

// diagnosticsRefusalEnvelope marshals the minimal bounded error envelope:
// version plus error. It is valid JSON, never tail-cut, and small enough to
// fit any realistic budget (mirrors nav_json.go's fallback contract). The
// consumer's diagnosticsEnvelope unmarshal treats the missing rows/summary
// fields as empty.
func diagnosticsRefusalEnvelope(msg string) string {
	data, err := json.Marshal(struct {
		Version int    `json:"version"`
		Error   string `json:"error"`
	}{Version: diagnosticsEnvelopeVersion, Error: msg})
	if err != nil {
		// Unreachable: the shape is a fixed static struct.
		return `{"version":1,"error":"get_diagnostics failed"}`
	}
	return string(data)
}
