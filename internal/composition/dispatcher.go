package composition

import (
	"context"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/hooks"
	"github.com/MiviaLabs/mivia-agent/internal/redact"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// DispatcherInput carries the tool registry, dispatcher policy, and hook
// wiring BuildDispatcher needs to assemble a runtime.Dispatcher. The policy
// fields mirror runtime.Policy field for field; BuildDispatcher is the only
// place that translates one into the other.
type DispatcherInput struct {
	// Registry is the tool registry the dispatcher registers handlers
	// against.
	Registry *tools.Registry

	MaxDepth, MaxRetries, MaxInputBytes, MaxOutputBytes int
	MaxBudget                                           int
	// Allow is the per-Kind, per-name allow map. Nil allows every
	// registered handler, the same default runtime.Policy carries today.
	Allow map[runtime.Kind]map[string]bool
	// Sink, when set, receives one runtime.Event per invocation lifecycle
	// step.
	Sink func(runtime.Event)

	// WorkspaceRoot is the directory lifecycle hooks execute in. Empty
	// means no hooks are wired at all, the same as HooksConfigured false.
	WorkspaceRoot string
	// HooksConfigured reports whether this session has any hook armed,
	// evaluated once at build time. False (or an empty WorkspaceRoot)
	// means Policy.PreInvokeHook/PostInvokeHook stay nil: one nil compare
	// per invocation, no hook overhead at all - the same contract
	// internal/cli/hooks_runner.go's hookPolicyFuncs held before this
	// move.
	HooksConfigured bool
	// HookGroups returns the runnable hook groups for the current session.
	// It is read fresh on every hook invocation rather than closed over at
	// build time, so what the caller's session state lists is what the
	// next tool call runs without a dispatcher rebuild. Required when
	// HooksConfigured is true.
	HookGroups func() []hooks.Group
	// NoteHookWarnings receives runtime diagnostics from hooks that
	// actually executed, for the caller to surface (e.g. a /hooks
	// listing). Nil is safe: warnings are simply dropped.
	NoteHookWarnings func([]string)
}

// BuildDispatcher assembles the runtime.Dispatcher with hook gates. It fills
// Policy.PreInvokeHook/PostInvokeHook exactly as internal/cli/hooks_runner.go
// did before this move: nil when no hooks are configured, otherwise closures
// that run the session's hook groups through a hooks.Runner rooted at
// WorkspaceRoot.
func BuildDispatcher(in DispatcherInput) (*runtime.Dispatcher, error) {
	pre, post := HookPolicyFuncs(in)
	d, err := runtime.NewToolDispatcher(in.Registry, runtime.Policy{
		MaxDepth:       in.MaxDepth,
		MaxRetries:     in.MaxRetries,
		MaxInputBytes:  in.MaxInputBytes,
		MaxOutputBytes: in.MaxOutputBytes,
		MaxBudget:      in.MaxBudget,
		Allow:          in.Allow,
		Sink:           in.Sink,
		PreInvokeHook:  pre,
		PostInvokeHook: post,
	})
	if err != nil {
		return nil, fmt.Errorf("create tool dispatcher: %w", err)
	}
	return d, nil
}

// HookPolicyFuncs returns the dispatcher's lifecycle-hook fields, or nils
// when no hook is configured at all. Exported so a caller that needs the
// funcs without a full dispatcher (e.g. a session that rebuilds Policy
// directly) can still delegate to this package rather than reimplement the
// hook-execution logic.
//
// Nil is not an optimisation, it is the contract: with no hooks configured
// the dispatcher does one nil compare per invocation and behaves exactly as
// it did before this layer existed.
func HookPolicyFuncs(in DispatcherInput) (
	func(context.Context, runtime.Request) runtime.HookVerdict,
	func(context.Context, runtime.Request, runtime.Result) runtime.HookResult,
) {
	if in.WorkspaceRoot == "" || !in.HooksConfigured {
		return nil, nil
	}
	runner := hooks.Runner{WorkspaceRoot: in.WorkspaceRoot}
	pre := func(ctx context.Context, req runtime.Request) runtime.HookVerdict {
		outcome := runHookEvent(ctx, runner, hooks.EventPreToolUse, req, in.HookGroups, in.NoteHookWarnings)
		return runtime.HookVerdict{
			Denied:  outcome.Denied,
			Reason:  outcome.Reason,
			Context: outcome.Context,
			Runs:    hookRunsFor(outcome, req.Input),
		}
	}
	post := func(ctx context.Context, req runtime.Request, _ runtime.Result) runtime.HookResult {
		outcome := runHookEvent(ctx, runner, hooks.EventPostToolUse, req, in.HookGroups, in.NoteHookWarnings)
		return runtime.HookResult{Context: outcome.Context, Runs: hookRunsFor(outcome, req.Input)}
	}
	return pre, post
}

// maxHookRunInputBytes bounds the tool input recorded onto a HookRun. It is
// retained in the dispatcher's dedup completed map for the life of the turn,
// not just at display time, so it must already be bounded and redacted
// before it is set - not only when a renderer later reads it.
const maxHookRunInputBytes = 512

// hookRunsFor translates the hook layer's execution records into the
// runtime's display type. The two are separate types on purpose:
// internal/runtime imports internal/hooks nowhere, and a shared struct
// would be the import that starts.
//
// toolInput is the tool call's own input (identical for every run in one
// outcome, since they all fired for the same call); it is bounded and
// redacted here rather than at emit time, because the unredacted value must
// never be held at all, even before anything renders it.
func hookRunsFor(outcome hooks.Outcome, toolInput json.RawMessage) []runtime.HookRun {
	if len(outcome.Runs) == 0 {
		return nil
	}
	input := boundedRedactedInput(toolInput)
	runs := make([]runtime.HookRun, 0, len(outcome.Runs))
	for _, run := range outcome.Runs {
		runs = append(runs, runtime.HookRun{
			Event:   string(run.Event),
			Program: run.Program,
			Tool:    run.Tool,
			Input:   input,
			Denied:  run.Denied,
			Output:  run.Output,
			Warning: run.Warning,
		})
	}
	return runs
}

// boundedRedactedInput redacts secret-shaped values out of a tool input and
// truncates it to maxHookRunInputBytes before it is ever retained.
func boundedRedactedInput(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	redacted := redact.Text(string(input))
	if len(redacted) <= maxHookRunInputBytes {
		return redacted
	}
	truncated := redacted[:maxHookRunInputBytes]
	// Repair a cut that landed mid-rune: redact.Text may have grown or
	// shrunk byte offsets relative to input, so re-validate at the bound
	// rather than assume the original string's rune boundaries still apply.
	for !utf8.ValidString(truncated) && len(truncated) > 0 {
		truncated = truncated[:len(truncated)-1]
	}
	return truncated + "...(truncated)"
}

// runHookEvent runs one lifecycle event's hook groups, read fresh from
// hookGroups, and reports any run warnings to noteWarnings.
func runHookEvent(
	ctx context.Context,
	runner hooks.Runner,
	event hooks.Event,
	req runtime.Request,
	hookGroups func() []hooks.Group,
	noteWarnings func([]string),
) hooks.Outcome {
	var groups []hooks.Group
	if hookGroups != nil {
		groups = hookGroups()
	}
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
	// goroutine while a UI may own the terminal, so writing to stderr here
	// could garble the screen mid-render; the caller's own listing (e.g.
	// /hooks) reports them instead.
	if noteWarnings != nil {
		noteWarnings(outcome.Warnings)
	}
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
