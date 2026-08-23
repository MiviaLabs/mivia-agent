package sdkadapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/hooks"
)

// HookRunner is the surface the bridge uses from the CLI hooks package. It
// matches hooks.Runner.Run by shape; declaring it here lets tests pass a
// fake without spinning subprocesses. A production bridge calls
// hooks.Runner.Run verbatim.
type HookRunner interface {
	Run(ctx context.Context, groups []hooks.Group, payload hooks.Payload) hooks.Outcome
}

// Handler is one adapter entry the SDK's hooks.Registry.Fire can call. It
// accepts the SDK-side signature (ctx, any) and returns the SDK-side
// verdict (allow bool, error). Map the CLI Outcome onto that verdict at
// every invocation; see Handle for the rules.
type Handler struct {
	runner HookRunner
	groups []hooks.Group
}

// NewHandler builds a Handler for the supplied runner and CLI hook groups.
// The runner is the production hooks.Runner value (a struct, not a
// pointer) or a test fake; the bridge never mutates either.
func NewHandler(runner HookRunner, groups []hooks.Group) *Handler {
	return &Handler{runner: runner, groups: groups}
}

// Handle runs the CLI hook groups for one SDK-shaped call and returns the
// verdict the SDK's Registry.Fire expects.
//
// Mapping rules:
//
//   - Reactive events (PostToolUse, Stop) cannot block; always (true, nil).
//   - PreToolUse with Denied==true and a Reason is a veto:
//     return (false, nil) so the wrapping Registry.Fire raises ErrVetoed.
//   - PreToolUse with Denied==true and no Reason is a defect: return an
//     error wrapping the empty-Reason signal so a missing-reason denial
//     reaches the wire as a real failure rather than as an unsourced veto.
//   - Otherwise the call is allowed: return (true, nil). Warnings are
//     operator-facing diagnostics and never reach the SDK veto path.
func (h *Handler) Handle(ctx context.Context, payload any) (bool, error) {
	converted, err := PayloadFromAny(payload)
	if err != nil {
		return false, fmt.Errorf("sdkadapter: hooks: payload conversion: %w", err)
	}
	outcome := h.runner.Run(ctx, h.groups, converted)
	reactive := isReactive(converted.Event)
	switch {
	case reactive:
		return true, nil
	case outcome.Denied:
		if outcome.Reason == "" {
			return false, errors.New("sdkadapter: hooks: CLI hook denied the call without a reason")
		}
		return false, nil
	default:
		return true, nil
	}
}

// PayloadFromAny converts an SDK-supplied payload into the CLI's
// hooks.Payload shape. Two payload formats are accepted: a
// map[string]any (the natural shape from a SDK handler call site) and a
// JSON-encoded byte slice (the wire shape an SDK transport produces).
// Any other type is rejected with a descriptive error.
func PayloadFromAny(payload any) (hooks.Payload, error) {
	switch v := payload.(type) {
	case hooks.Payload:
		return v, nil
	case hooks.Outcome:
		// Allow Outcome-shaped payloads to be re-fed into a fresh handler
		// unchanged. Outcome is a CLI-only concept and is converted by the
		// bridge into verdict calls; reaching PayloadFromAny with an Outcome
		// means a caller that already ran the runner is asking the bridge to
		// re-run, which we refuse to do, but a future handler might want it.
		return hooks.Payload{}, fmt.Errorf("sdkadapter: hooks: Outcome-shaped payload unsupported")
	case map[string]any:
		return payloadFromMap(v), nil
	case []byte:
		return payloadFromBytes(v)
	case json.RawMessage:
		return payloadFromBytes([]byte(v))
	case string:
		return payloadFromBytes([]byte(v))
	default:
		return hooks.Payload{}, fmt.Errorf("sdkadapter: hooks: payload type %T not supported", payload)
	}
}

// payloadFromMap hoists the SDK-shape keys onto the CLI's hooks.Payload
// fields. Unknown keys are dropped on purpose: the CLI's runner reads
// only the well-known fields and silently ignoring the rest is the
// safe default for a bridge. The Event field is matched against the
// CLI's three allowed values; an unknown event lands as an empty Event,
// which the runner's Matches filter then ignores, which is the same
// outcome as a no-match handler group firing for no event.
func payloadFromMap(in map[string]any) hooks.Payload {
	out := hooks.Payload{}
	if v, ok := in["event"].(string); ok {
		out.Event = hooks.Event(v)
	}
	if v, ok := in["tool"].(string); ok {
		out.Tool = v
	}
	if v, ok := in["session_id"].(string); ok {
		out.SessionID = v
	}
	if v, ok := in["turn_id"].(string); ok {
		out.TurnID = v
	}
	if v, ok := in["tool_call_id"].(string); ok {
		out.ToolCallID = v
	}
	if v, ok := in["input"].(json.RawMessage); ok {
		out.Input = v
	} else if v, ok := in["input"]; ok {
		// JSON raw message arrived through a map-of-any unmarshal, which
		// gives us a string; re-encode it.
		switch typed := v.(type) {
		case string:
			out.Input = json.RawMessage(typed)
		case []byte:
			out.Input = json.RawMessage(typed)
		}
	}
	return out
}

// payloadFromBytes decodes a JSON-encoded payload into the CLI shape. A
// non-JSON byte slice is rejected with an error; the bridge must always
// see the JSON shape so the field hoisting matches payloadFromMap.
func payloadFromBytes(data []byte) (hooks.Payload, error) {
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return hooks.Payload{}, fmt.Errorf("sdkadapter: hooks: payload bytes did not parse as JSON: %w", err)
	}
	return payloadFromMap(m), nil
}

// isReactive reports whether the CLI event is reactive (cannot veto a
// tool call that already ran). Only PreToolUse can block; PostToolUse and
// Stop are observation-only by hooks.go's design.
func isReactive(event hooks.Event) bool {
	return event == hooks.EventPostToolUse || event == hooks.EventStop
}
