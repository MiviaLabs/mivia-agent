package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

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
	startLen := len(msgs)
	res, err := RunAgentLoopOnce(ctx, l, opts, msgs)
	// Write the turn's history back even on error: the legacy path keeps
	// the user message and every completed step's messages in l.Messages
	// when a turn fails mid-flight, and callers persist the partial turn
	// from the loop. The SDK returns a partial History with its error.
	if len(res.History) > 0 {
		l.Messages = restoreSDKHistoryTimestamps(sdkMessagesToCLI(res.History), l.Messages)
	}
	if err != nil {
		// A canceled or timed-out run keeps the partial reply the turn
		// already produced, mirroring the legacy interrupted-step
		// contract (Loop.Run returns lastText on an interrupted step;
		// the subagent pool maps this to status canceled/timed_out and
		// keeps the output). Every other error returns empty so a raw
		// provider body cannot leak (the pinned guarantee in
		// TestMultiStepHandlerFailureOmitsRawProviderBodyAndRefs).
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			for i := len(res.History) - 1; i >= startLen; i-- {
				if m := res.History[i]; m.Role == sdkshape.RoleAssistant && strings.TrimSpace(m.Content) != "" {
					return m.Content, err
				}
			}
		}
		return "", err
	}
	if res.Stop == sdkagentloop.StopSteered {
		return "", errSteerInterrupt
	}
	if res.Stop == sdkagentloop.StopMaxIterations {
		// Legacy parity: exceeding the step cap is a hard error naming
		// the cap, not a graceful partial answer.
		return "", fmt.Errorf("agent exceeded max_steps (%d)", effectiveSDKMaxIterations(opts))
	}
	if strings.TrimSpace(res.Final.Content) == "" {
		// Mirror the legacy lastText fallback, scoped to THIS turn: a
		// graceful stop whose final step produced no text returns the
		// last assistant text the turn produced, never a prior turn's.
		for i := len(res.History) - 1; i >= startLen; i-- {
			if m := res.History[i]; m.Role == sdkshape.RoleAssistant && strings.TrimSpace(m.Content) != "" {
				return m.Content, nil
			}
		}
	}
	return res.Final.Content, nil
}

// restoreSDKHistoryTimestamps copies CreatedAt from the pre-turn slice
// onto the converted history's matching prefix (the SDK Message shape
// carries no timestamp) and stamps the turn's new messages with the
// current time, matching the legacy path's time.Now() appends.
func restoreSDKHistoryTimestamps(fresh, old []provider.Message) []provider.Message {
	now := time.Now()
	for i := range fresh {
		if i < len(old) {
			fresh[i].CreatedAt = old[i].CreatedAt
		} else if fresh[i].CreatedAt.IsZero() {
			fresh[i].CreatedAt = now
		}
	}
	return fresh
}

// effectiveSDKMaxIterations mirrors buildAgentLoopOptions' iteration
// clamp so an error message can name the same cap the SDK enforced.
func effectiveSDKMaxIterations(opts Options) int {
	limit := opts.MaxSteps
	if limit <= 0 {
		limit = defaultSDKMaxIterations
	}
	if wl := opts.WorkLimits.MaxTurns; wl > 0 && (opts.MaxSteps <= 0 || wl < opts.MaxSteps) {
		limit = wl
	}
	return limit
}
