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

// servedUnadmittedToolMessage builds the RoleTool message for a deferred
// call UnadmittedToolHandler served synchronously: rendered exactly like an
// ordinary successful call - no "error: " prefix, no failed tool_end, the
// error text this path exists to remove. appendHookContext gives the model
// the same framed, tag-neutralized advisory text dispatcherShim.Run gives
// it for an admitted call - the recorded outcome and the returned message
// carry the SAME body, exactly like the shim's capped+appended body.
func servedUnadmittedToolMessage(turn *sdkTurnState, callKey string, call sdkshape.ToolCall, result UnadmittedToolResult) sdkshape.Message {
	body := appendHookContext(result.Content, result.HookContext)
	if turn != nil {
		turn.recordToolOutcomeWithPreview(callKey, call.Name, body, false, "", false, body)
	}
	return sdkshape.Message{
		Role:       provider.RoleTool,
		ToolCallID: call.ID,
		Name:       call.Name,
		Content:    body,
	}
}

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
func sdkToolCallErrorReporter(opts Options, turn *sdkTurnState) sdkagentloop.ErrorFunc {
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
		callKey := call.ID
		if callKey == "" {
			callKey = call.Name
		}
		msg := ""
		if opts.StagedToolMessage != nil {
			if m, ok := opts.StagedToolMessage(call.Name); ok {
				msg = m
			}
		}
		// StagedToolMessage already found a pending stage; the handler below
		// only runs for a genuinely fresh deferred call, so it is never asked
		// to synchronously execute a call StagedToolMessage is about to deny.
		if msg == "" && opts.UnadmittedToolHandler != nil {
			result := opts.UnadmittedToolHandler(ctx, call.Name, call.Arguments)
			// The deferred-tool analogue of dispatcherShim.Run's emitHookRuns:
			// this path reaches the dispatcher too (Policy lives on the
			// Dispatcher, not on the caller), so its hooks really ran. Covers
			// both branches below - a served call AND a PreToolUse-blocked
			// one, which is exactly the run an operator needs to see. The
			// callKey guard mirrors sdk_dispatcher_shim.go's; HookRuns is
			// already nil for a dedup-served duplicate (runDeferredToolNow).
			if callKey != "" {
				emitHookRuns(opts, callKey, result.HookRuns)
			}
			if result.Handled && result.Ran {
				return servedUnadmittedToolMessage(turn, callKey, call, result), nil
			}
			if result.Handled {
				msg = result.Content
			}
		}
		if msg == "" {
			msg = fmt.Sprintf("tool %q is not available to this agent", call.Name)
		}
		// Record a failed outcome so the tool-event synthesis renders
		// the denial as the same failed tool_end the legacy path emits
		// (sdk_tool_events.go); without it the deferred
		// EventToolCallEnd would find neither outcome nor pending.
		// duplicate=false, originalBody="" (the denial IS the recorded
		// body here, so failed-duplicate detection has nothing to scan
		// against; in practice a denial never has a prior duplicate to
		// pair with).
		if turn != nil {
			turn.recordToolOutcomeWithPreview(callKey, call.Name, "error: "+msg, true, "", false, "")
		}
		return sdkshape.Message{
			Role:       provider.RoleTool,
			ToolCallID: call.ID,
			Name:       call.Name,
			Content:    "error: " + msg,
		}, nil
	}
}
