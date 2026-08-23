package agent

// SDK-path mirror of the legacy not-in-registry tool denial
// (loop_tool_exec.go's executeToolTask): StagedToolMessage first,
// then UnadmittedToolHandler, then the generic not-available message,
// each rendered as "error: <msg>". On the SDK path a call naming a
// non-registry tool dies inside the SDK's decodeAndRun before any
// host wrapper runs, so the denial rides the SDK's
// Options.OnToolCallError hook instead.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	sdkagentloop "github.com/MiviaLabs/mivia-ai-sdk/agentloop"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
	sdktools "github.com/MiviaLabs/mivia-ai-sdk/tools"
)

// sdkToolCallErrorReporter returns the Options.OnToolCallError hook
// that mirrors the legacy executeToolTask not-in-registry branch:
// when the SDK failed the call because the tool is unknown or was
// not offered (tools.ErrUnknownName / sdkagentloop.ErrToolNotOffered),
// the RoleTool body becomes "error: " plus, in order, the
// StagedToolMessage notice, the UnadmittedToolHandler message, or the
// generic "tool %q is not available to this agent" text - the exact
// strings the legacy loop renders. Any other tool-run error returns a
// zero Message and nil error so the SDK keeps its default
// [tool-error] body (ordinary decode/render failures are unchanged).
//
// Accepted divergence from the legacy path (reviewer amendment 6): the
// denial content skips the legacy result-budget charging and
// truncation (BatchResultBudgetBytes / MaxToolResultChars shaping)
// because the hook replaces the tool-result body before any host
// shaping wrapper runs; a short fixed denial notice is not shaped.
func sdkToolCallErrorReporter(opts Options) sdkagentloop.ErrorFunc {
	return func(ctx context.Context, call sdkshape.ToolCall, runErr error) (sdkshape.Message, error) {
		if !(errors.Is(runErr, sdktools.ErrUnknownName) || errors.Is(runErr, sdkagentloop.ErrToolNotOffered)) {
			return sdkshape.Message{}, nil
		}
		// Legacy precedence: processToolCalls filters malformed-JSON
		// arguments (loop_tools.go) BEFORE executeToolTask's
		// not-in-registry branch, so a malformed call never received
		// the denial text. Mirror that: fall through to the SDK's
		// default [tool-error] body when the arguments are not JSON,
		// preserving the SDK path's documented malformed-call contract.
		if strings.TrimSpace(string(call.Arguments)) != "" && !json.Valid(call.Arguments) {
			return sdkshape.Message{}, nil
		}
		msg := ""
		if opts.StagedToolMessage != nil {
			if m, ok := opts.StagedToolMessage(call.Name); ok {
				msg = m
			}
		}
		if msg == "" && opts.UnadmittedToolHandler != nil {
			if m, ok := opts.UnadmittedToolHandler(ctx, call.Name); ok {
				msg = m
			}
		}
		if msg == "" {
			msg = fmt.Sprintf("tool %q is not available to this agent", call.Name)
		}
		return sdkshape.Message{
			Role:       provider.RoleTool,
			ToolCallID: call.ID,
			Name:       call.Name,
			Content:    "error: " + msg,
		}, nil
	}
}
