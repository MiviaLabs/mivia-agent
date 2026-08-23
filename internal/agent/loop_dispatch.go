package agent

import (
	"context"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	sdkagentloop "github.com/MiviaLabs/mivia-ai-sdk/agentloop"
)

// runOnce is the flag-dispatched driver behind (*Loop).Run. opts.Backend
// picks the inner loop: the legacy branch is the unchanged pre-flag body,
// and the sdk branch drives the SDK-backed loop through
// RunAgentLoopOnce (completer wrapper, registry converter, steer
// bridge, and the fail-closed Options checks all live there).
func (l *Loop) runOnce(ctx context.Context, userText string, opts Options) (string, error) {
	switch opts.Backend {
	case "", "legacy":
		return l.runOnceLegacy(ctx, userText, opts)
	case "sdk":
		return l.runOnceSDK(ctx, userText, opts)
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
	msgs := make([]provider.Message, 0, len(l.Messages)+1)
	msgs = append(msgs, l.Messages...)
	msgs = append(msgs, provider.Message{Role: provider.RoleUser, Content: userText})
	res, err := RunAgentLoopOnce(ctx, l, opts, msgs)
	if err != nil {
		return "", err
	}
	if res.Stop == sdkagentloop.StopSteered {
		return "", errSteerInterrupt
	}
	return res.Final.Content, nil
}
