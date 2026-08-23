// Package agent - SDK agent-loop adapter.
//
// RunAgentLoop drives the SDK's mivia-ai-sdk/agentloop.Loop from the
// CLI's Options shape. It is ADDITIVE: the legacy (*Loop).Run in
// loop.go is unchanged. B.2 #8 minimum-viable ships the bridge only;
// the chat surface wires RunAgentLoop as the inner loop in a
// follow-up commit.
//
// RunAgentLoop is the first consumer of the SDK's *Loop, and the
// first place the SDK's Loop.Run signature (func(ctx, []Message)
// (Result, error)) is called from CLI code. The adapter projects
// Options onto the SDK's agentloop.Options, then returns the SDK's
// Result so a caller can read Final, History, Iterations, Usage, and
// Stop through the canonical SDK shape.
//
// The SDK imports are out-of-prefix; the gate filters them out of
// the in-prefix edge set (scripts/check_import_layers.py
// compute_edges), and the policy baseline is unchanged.
package agent

import (
	"context"

	sdkagentloop "github.com/MiviaLabs/mivia-ai-sdk/agentloop"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
)

// buildAgentLoopOptions projects a CLI Options onto the SDK's
// agentloop.Options. Only the fields with a 1:1 mapping are
// projected; the rest stay at their SDK zero values. This is
// intentional: the legacy (*Loop).Run keeps working unchanged for
// every field the SDK does not carry.
func buildAgentLoopOptions(opts Options) sdkagentloop.Options {
	return sdkagentloop.Options{
		Model:         opts.Model,
		MaxIterations: opts.MaxSteps,
	}
}

// RunAgentLoop drives the SDK's agentloop.Loop for one Options. It
// builds the SDK options, calls New, and returns the SDK Run result.
// The legacy (*Loop).Run in loop.go is NOT replaced; this is an
// additive path that B.2 #8's chat-surface wiring will use.
//
// opts.Reasoning.Level maps onto the SDK Request's ReasoningEffort
// through provider.Request.SDKReasoningEffort, which
// internal/provider/reasoning.go's encoder populates exactly once
// when a request is encoded. The adapter does not translate
// Level->Effort here; that mapping happens at the request encoder
// call site so the same level-to-effort decision governs every
// Completer call the SDK loop makes.
func RunAgentLoop(ctx context.Context, opts Options) (sdkagentloop.Result, error) {
	sdkOpts := buildAgentLoopOptions(opts)
	loop, err := sdkagentloop.New(sdkOpts)
	if err != nil {
		return sdkagentloop.Result{}, err
	}
	return loop.Run(ctx, nil)
}

// Compile-time check: SDK's Completer type is reachable from the
// adapter package through the same alias the bridge package uses.
var _ sdkshape.Completer
