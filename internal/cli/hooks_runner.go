package cli

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/hooks"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

// hookPolicyFuncs returns the dispatcher's lifecycle-hook fields, or nils when
// no hook is configured at all.
//
// Nil is not an optimisation, it is the contract: with no hooks configured the
// dispatcher does one nil compare per invocation and behaves exactly as it did
// before this layer existed. When hooks ARE configured but none is trusted yet,
// the funcs are installed anyway - the trust decision belongs to the runner, so
// /hooks trust takes effect on the next tool call rather than on the next
// dispatcher rebuild.
func hookPolicyFuncs(workspaceRoot string) (
	func(context.Context, runtime.Request) runtime.HookVerdict,
	func(context.Context, runtime.Request, runtime.Result) string,
) {
	if workspaceRoot == "" || !hookSessionConfigured() {
		return nil, nil
	}
	runner := hooks.Runner{WorkspaceRoot: workspaceRoot}
	pre := func(ctx context.Context, req runtime.Request) runtime.HookVerdict {
		outcome := runHookEvent(ctx, runner, hooks.EventPreToolUse, req)
		return runtime.HookVerdict{Denied: outcome.Denied, Reason: outcome.Reason, Context: outcome.Context}
	}
	post := func(ctx context.Context, req runtime.Request, _ runtime.Result) string {
		return runHookEvent(ctx, runner, hooks.EventPostToolUse, req).Context
	}
	return pre, post
}

// runStopHookEvent fires Stop hooks for a completed ROOT turn and returns their
// output as an attributed continuation prompt.
//
// Stop is pure observation: it has no denial channel at all, so a Stop hook can
// log a turn's cost and can never affect whether the turn ended.
//
// Scope, stated rather than assumed: KindTurnEnd's only publish site is the
// root TUI turn goroutine (internal/cli/tui_events.go), so Stop fires once per
// user-visible turn and never per subagent turn - a per-subagent Stop would run
// N times and its "the assistant is done" semantics would be false every time
// but the last. The same fact bounds the feature: the classic --plain REPL and
// the -p one-shot never publish KindTurnEnd, so Stop does not fire there. That
// is a seam gap, not a design choice, and it is recorded here rather than
// papered over. -p is headless anyway, where no hook runs without the bypass.
//
// The context is the turn's own, deliberately not a detached one. A canceled
// turn therefore does not run its Stop hook, and the run is RECORDED rather
// than skipped silently - detaching would make Ctrl-C wait out the hook's
// timeout before the cancelled footer appeared.
func runStopHookEvent(ctx context.Context, workspaceRoot, sessionID, turnID string) string {
	session := currentHookSession()
	groups := session.runnable()
	if len(groups) == 0 {
		return ""
	}
	outcome := hooks.Runner{WorkspaceRoot: workspaceRoot}.Run(ctx, groups, hooks.Payload{
		Event:     hooks.EventStop,
		SessionID: sessionID,
		TurnID:    turnID,
	})
	session.noteRunWarnings(outcome.Warnings)
	return outcome.Context
}

func runHookEvent(ctx context.Context, runner hooks.Runner, event hooks.Event, req runtime.Request) hooks.Outcome {
	session := currentHookSession()
	groups := session.runnable()
	if len(groups) == 0 {
		return hooks.Outcome{}
	}
	outcome := runner.Run(ctx, groups, hooks.Payload{
		Event:      event,
		Tool:       req.Name,
		Input:      req.Input,
		SessionID:  req.SessionID,
		TurnID:     req.TurnID,
		ToolCallID: req.ID,
		File:       hookFileFromInput(req.Input),
	})
	// Warnings are recorded rather than printed. A tool call runs on its own
	// goroutine while the TUI owns the terminal, so writing to stderr here would
	// garble the screen mid-render; /hooks reports them instead.
	session.noteRunWarnings(outcome.Warnings)
	return outcome
}

// hookFileFromInput extracts the path a tool is acting on, for MIVIA_FILE.
//
// It reads a top-level string "path" and nothing else - deliberately not a
// search through nested structures. The value is exported through the
// environment and never spliced into an argv, so a filename containing shell
// syntax is inert either way; a narrow, predictable rule is what lets a hook
// author reason about when the variable is set.
func hookFileFromInput(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var fields struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(input, &fields); err != nil {
		return ""
	}
	return fields.Path
}

// warnHookLoad surfaces startup-time hook diagnostics.
func formatHookWarning(warning string) string { return fmt.Sprintf("warning: %s", warning) }
