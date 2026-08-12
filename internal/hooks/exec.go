package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// MaxOutputBytes bounds the total hook-supplied context one invocation may
// produce, across every handler that ran. Over-budget output is TRUNCATED with
// a notice rather than refused: unlike tool output, which the dispatcher
// destroys because an undeclared result cannot be bounded, hook stdout is
// advisory, and destroying a tool's result because its formatter was chatty is
// the worse failure.
const MaxOutputBytes = 8 << 10

// maxReasonBytes bounds a block reason. The reason reaches the model, so it is
// bounded for the same reason every other model-visible payload is.
const maxReasonBytes = 4 << 10

// waitGrace is how long a killed hook is given to be reaped before its slot is
// abandoned. A process wedged in an unkillable state must not hold the turn.
const waitGrace = 5 * time.Second

// Payload is the JSON object written to a hook's stdin. The field names mirror
// Claude Code and Codex so hook scripts port between harnesses.
type Payload struct {
	Event      Event           `json:"event"`
	Tool       string          `json:"tool,omitempty"`
	Input      json.RawMessage `json:"input,omitempty"`
	SessionID  string          `json:"session_id,omitempty"`
	TurnID     string          `json:"turn_id,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`

	// File is exported to the hook as MIVIA_FILE. It is never spliced into an
	// argv, so a filename containing shell syntax is inert.
	File string `json:"-"`
}

// Outcome aggregates every handler that ran for one event.
type Outcome struct {
	// Denied is true only for PreToolUse. Reactive events cannot block.
	Denied bool
	// Reason is the block reason. It reaches the model - that is the entire
	// point of a block.
	Reason string
	// Context is hook-supplied advisory text, bounded by MaxOutputBytes.
	Context string
	// Warnings are operator-facing diagnostics; they never reach the model.
	Warnings []string
	// Runs records every handler that actually executed, in order.
	//
	// It exists so the operator can see a hook fire. Without it, a hook that
	// runs on every write is invisible until it says something, and "did my
	// formatter run?" has no answer short of instrumenting the script - which
	// is how a silently mis-matched matcher survives for weeks.
	Runs []Run
}

// Run is one handler execution, recorded for display.
//
// It is deliberately separate from Context: Context is what the MODEL is told,
// bounded and merged across handlers, while a Run is what the OPERATOR is
// shown, attributed to the script that produced it. Merging the two would mean
// either showing the operator less than happened or telling the model more than
// it needs.
type Run struct {
	Event   Event
	Tool    string
	Program string
	Denied  bool
	// Output is what this handler produced: its advisory text, or the reason it
	// blocked. Empty means it ran and said nothing, which is the normal case
	// for a formatter and is still worth showing.
	Output string
	// Warning is the operator diagnostic this handler produced, if it
	// misbehaved - a timeout, a crash, an exit code with no decision in it.
	Warning string
}

// Runner executes hook commands out-of-band.
//
// Hooks deliberately do NOT reuse run_command's execution path. That path
// refuses a path-shaped argv[0], requires the program to be on the run
// allowlist, pins cwd to the workspace root, refuses secret-like paths, and
// wraps output in a command:/cwd:/exit= header that would corrupt the JSON
// protocol. Adding hook scripts to the run allowlist to make them fit would be
// worse: it would hand the MODEL the ability to invoke them.
type Runner struct {
	// WorkspaceRoot is the working directory hook commands run in.
	WorkspaceRoot string
}

// Run executes every handler in groups whose event and matcher select payload.
//
// PreToolUse stops at the first deny: the call is not happening, and the
// remaining handlers have side effects. Reactive events run every handler,
// because none of them can veto and each exists for its own effect.
func (r Runner) Run(ctx context.Context, groups []Group, payload Payload) Outcome {
	var out Outcome
	var body strings.Builder
	truncated := false
	for _, group := range groups {
		if group.Event != payload.Event || !group.Matches(payload.Tool) {
			continue
		}
		for _, handler := range group.Handlers {
			execution := r.execute(ctx, group, handler, payload)
			verdict := classify(payload.Event, handler, execution)
			out.Warnings = append(out.Warnings, verdict.warnings...)
			out.Runs = append(out.Runs, runRecord(payload, execution, verdict))
			truncated = appendBounded(&body, verdict.context) || verdict.truncated || truncated
			if verdict.denied {
				out.Denied = true
				out.Reason = verdict.reason
				out.Context = finishContext(body.String(), truncated)
				return out
			}
		}
	}
	out.Context = finishContext(body.String(), truncated)
	return out
}

// runRecord builds the display record for one handler.
//
// A blocked call reports the block REASON as its output. For the operator the
// two are the same question - what did this hook say? - and a record that
// showed a denial with no text would be the least useful line on the screen.
func runRecord(payload Payload, execution execution, verdict verdict) Run {
	output := verdict.context
	if verdict.denied {
		output = verdict.reason
	}
	record := Run{
		Event:   payload.Event,
		Tool:    payload.Tool,
		Program: execution.label(),
		Denied:  verdict.denied,
		Output:  strings.TrimSpace(output),
	}
	if len(verdict.warnings) > 0 {
		record.Warning = verdict.warnings[0]
	}
	return record
}

// appendBounded adds text within MaxOutputBytes and reports whether anything
// was cut. Truncating is deliberate: hook advice is worth less than the tool
// result it accompanies, and refusing over-budget advice would lose both.
func appendBounded(body *strings.Builder, text string) bool {
	if text == "" {
		return false
	}
	if body.Len() > 0 {
		text = "\n" + text
	}
	room := MaxOutputBytes - body.Len()
	if room <= 0 {
		return true
	}
	if len(text) > room {
		body.WriteString(truncateAtRuneBoundary(text, room))
		return true
	}
	body.WriteString(text)
	return false
}

func finishContext(body string, truncated bool) string {
	if !truncated {
		return body
	}
	return body + fmt.Sprintf("\n... hook output truncated at %d bytes", MaxOutputBytes)
}

// execution is one handler's raw result, before event semantics apply.
type execution struct {
	program  string
	stdout   []byte
	stderr   []byte
	exitCode int
	// noVerdict is set when the handler produced no answer at all - it could
	// not start, or it was killed. Both resolve through OnTimeout.
	noVerdict bool
	reason    string
	// truncated is set when the hook wrote more than the capture bound. It is
	// carried rather than inferred from the captured length: output that lands
	// exactly on the bound is indistinguishable from output that overran it.
	truncated bool
}

// label is the hook's name for MODEL-visible text. The absolute path runs
// through the user's home directory and the model has no use for it; the
// operator-facing warnings keep the full path.
func (e execution) label() string { return filepath.Base(e.program) }

func (r Runner) execute(ctx context.Context, group Group, handler Handler, payload Payload) execution {
	program := resolveProgram(group, handler.Argv[0])
	result := execution{program: program}
	stdin, err := json.Marshal(payload)
	if err != nil {
		result.noVerdict, result.reason = true, fmt.Sprintf("could not encode the hook payload: %v", err)
		return result
	}
	callCtx, cancel := context.WithTimeout(ctx, handler.Timeout)
	defer cancel()

	// No PATH lookup: program is always a path, so exec resolves exactly the
	// file the config named. A hook must not become a different binary because
	// PATH changed.
	cmd := exec.CommandContext(callCtx, program, handler.Argv[1:]...)
	cmd.Dir = r.WorkspaceRoot
	cmd.Env = hookEnv(r.WorkspaceRoot, payload)
	cmd.Stdin = bytes.NewReader(stdin)
	outBuf := &boundedBuffer{limit: MaxOutputBytes}
	errBuf := &boundedBuffer{limit: maxReasonBytes}
	cmd.Stdout, cmd.Stderr = outBuf, errBuf
	cmd.WaitDelay = 2 * time.Second
	scope := prepareCommand(cmd)
	cmd.Cancel = func() error { return scope.cancel(cmd) }
	defer scope.cleanup()

	runErr := startAndWait(cmd, callCtx, scope)
	// Repair OUR cut, not the hook's bytes: a capture that stopped at the byte
	// bound can end mid-rune, and this text becomes model-visible.
	result.stdout, result.stderr = outBuf.trimmed(), errBuf.trimmed()
	result.truncated = outBuf.over
	if callCtx.Err() != nil {
		// The deadline fired, but that is only a timeout when the run actually
		// failed to produce a verdict. A hook that exited 0 or with a real exit
		// code in the same instant its deadline landed has an answer, and
		// discarding it here would turn an allow that arrived within budget
		// into a spurious block (DC-7/DC-9). The deadline case honors that
		// verdict identically to a deadline-free run. The genuine timeouts are
		// a non-ExitError runErr other than ErrWaitDelay (the unreapable
		// waitGrace case, or a start failure under an expired context) and a
		// signal death (ExitCode()==-1, the cancel's kill landing before exit).
		// ErrWaitDelay is excluded: os/exec returns it only for a SUCCESSFUL
		// exit whose orphaned descendants kept the pipes open, so its captured
		// verdict is as real under an expired deadline as it is without one.
		var exitErr *exec.ExitError
		if runErr != nil && !errorsIs(runErr, exec.ErrWaitDelay) && (!errorsAs(runErr, &exitErr) || exitErr.ExitCode() == -1) {
			result.noVerdict = true
			result.reason = fmt.Sprintf("hook %s timed out after %s", filepath.Base(program), handler.Timeout)
			if ctx.Err() != nil {
				result.reason = fmt.Sprintf("hook %s did not run: %v", filepath.Base(program), ctx.Err())
			}
			return result
		}
	}
	if runErr != nil {
		var exitErr *exec.ExitError
		switch {
		case errorsAs(runErr, &exitErr):
			result.exitCode = exitErr.ExitCode()
		case errorsIs(runErr, exec.ErrWaitDelay):
			// os/exec returns ErrWaitDelay only when the process exited with a
			// SUCCESSFUL status but orphaned descendants kept the output pipes
			// open past WaitDelay. The captured stdout is therefore a real
			// verdict and must be honored, not discarded as a start failure.
			// ProcessState is set by os/exec before Wait returns, so
			// ExitCode() is authoritative here. An ErrWaitDelay under an
			// expired deadline reaches this same case: the deadline branch
			// above excludes it from the timeout classification, so it honors
			// the exit-0 verdict identically to a deadline-free run.
			result.exitCode = cmd.ProcessState.ExitCode()
		default:
			result.noVerdict = true
			result.reason = fmt.Sprintf("hook %s could not start: %v", filepath.Base(program), runErr)
			return result
		}
	}
	return result
}

func startAndWait(cmd *exec.Cmd, ctx context.Context, scope commandScope) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = scope.cancel(cmd)
		select {
		case err := <-done:
			return err
		case <-time.After(waitGrace):
			// The process is unreapable; abandon the Wait goroutine rather than
			// hold the turn. The kernel reclaims it when the process dies.
			return ctx.Err()
		}
	}
}

// resolveProgram turns argv[0] into a path. Relative paths resolve against the
// directory of the config file that declared the hook - not the workspace, and
// never through PATH.
func resolveProgram(group Group, argv0 string) string {
	if filepath.IsAbs(argv0) {
		return filepath.Clean(argv0)
	}
	return filepath.Join(filepath.Dir(group.Source), argv0)
}

// hookEnv is the process environment plus a fixed MIVIA_* set.
//
// Unlike run_command, hooks do NOT run under a filtered environment. The two
// cases differ in who chose the program: run_command executes an argv the MODEL
// composed, so its environment is narrowed; a hook is a program the user
// authored and explicitly confirmed, equivalent to something in their own shell
// profile, and stripping PATH or HOME would break ordinary scripts for no
// boundary gained.
//
// Tool-derived values reach the hook here and in the stdin JSON only. A value
// passed through the environment is never re-parsed as syntax.
func hookEnv(workspaceRoot string, payload Payload) []string {
	env := append(os.Environ(),
		"MIVIA_HOOK_EVENT="+string(payload.Event),
		"MIVIA_TOOL="+payload.Tool,
		"MIVIA_SESSION_ID="+payload.SessionID,
		"MIVIA_WORKSPACE_ROOT="+workspaceRoot,
		"MIVIA_FILE="+payload.File,
	)
	return env
}

// boundedBuffer keeps at most limit bytes and reports overflow. Writes past the
// bound still succeed so the hook is never stalled on a full pipe.
type boundedBuffer struct {
	buf   bytes.Buffer
	limit int
	over  bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	room := b.limit - b.buf.Len()
	switch {
	case room <= 0:
		b.over = b.over || len(p) > 0
	case len(p) > room:
		b.buf.Write(p[:room])
		b.over = true
	default:
		b.buf.Write(p)
	}
	return len(p), nil
}

// trimmed returns the captured bytes, dropping a trailing partial rune left by
// the bound. It repairs only a cut this buffer made.
func (b *boundedBuffer) trimmed() []byte {
	if !b.over {
		return b.buf.Bytes()
	}
	data := b.buf.Bytes()
	return []byte(truncateAtRuneBoundary(string(data), len(data)))
}
