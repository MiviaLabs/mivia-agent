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
