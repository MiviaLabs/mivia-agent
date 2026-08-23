package agent

import (
	"context"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	sdkagentloop "github.com/MiviaLabs/mivia-ai-sdk/agentloop"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
)

// runOnce is the flag-dispatched driver behind (*Loop).Run. opts.Backend
// picks the inner loop: the empty value (the default after the SDK
// convergence) and "sdk" both run the SDK-backed loop through
// RunAgentLoopOnce (completer wrapper, registry converter, steer
// bridge, and the fail-closed Options checks all live there); "legacy"
// runs the unchanged pre-SDK loop body for callers that still depend
// on options the SDK path cannot carry (Surface rotation, the four
// WorkLimits token reservations, BeforeStep).
func (l *Loop) runOnce(ctx context.Context, userText string, opts Options) (string, error) {
	switch opts.Backend {
	case "", "sdk":
		return l.runOnceSDK(ctx, userText, opts)
	case "legacy":
		return l.runOnceLegacy(ctx, userText, opts)
	default:
		return "", fmt.Errorf("agent: unknown Backend %q (want %q or %q)", opts.Backend, "legacy", "sdk")
	}
}

// runOnceSDK drives one SDK-backed turn and translates the SDK Result
// onto the legacy (string, error) contract. The message history is the
// loop's carried history plus the user's text, matching what the
// legacy path feeds its first provider call. A steered stop maps to
// the loop's existing errSteerInterrupt sentinel so callers handle
// both backends with one error identity; every other graceful stop
// returns the final assistant content.
func (l *Loop) runOnceSDK(ctx context.Context, userText string, opts Options) (string, error) {
	// Each Run owns its finish-reason report, mirroring runOnceLegacy's
	// reset: a previous run's reason must never leak into the next
	// caller's read. The SDK path leaves the field empty because the
	// SDK's Message shape carries no finish reason.
	l.LastFinishReason = ""
	msgs := make([]provider.Message, 0, len(l.Messages)+1)
	msgs = append(msgs, l.Messages...)
	msgs = append(msgs, provider.Message{Role: provider.RoleUser, Content: userText})
	res, err := RunAgentLoopOnce(ctx, l, opts, msgs)
	if err != nil {
		return "", err
	}
	// The SDK's History carries the starting messages plus every
	// assistant and tool message the turn appended. Replace the loop's
	// carried history with it so the next Run - and callers reading
	// l.Messages - see the turn's tool results, mirroring the legacy
	// path's incremental appends.
	l.Messages = sdkMessagesToCLI(res.History)
	if res.Stop == sdkagentloop.StopSteered {
		return "", errSteerInterrupt
	}
	if res.Final.Content == "" {
		// Mirror the legacy lastText fallback: a graceful stop whose
		// final step produced no text (StopMaxIterations, a vetoed
		// stop) returns the last assistant text the turn produced
		// instead of an empty answer.
		for i := len(res.History) - 1; i >= 0; i-- {
			if m := res.History[i]; m.Role == sdkshape.RoleAssistant && m.Content != "" {
				return m.Content, nil
			}
		}
	}
	return res.Final.Content, nil
}
