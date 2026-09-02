package sdkadapter

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-ai-sdk/toolcallctx"
	sdktools "github.com/MiviaLabs/mivia-ai-sdk/tools"
)

// callIDFromContext returns the in-flight call's id from
// toolcallctx, or "" when the ctx did not carry one (an SDK call
// outside the loop, or a hand-built test fixture). The pending event
// must carry the id so the UI's approval resolver can match a
// decision back to the gate that is blocked on it.
func callIDFromContext(ctx context.Context) string {
	if tc, ok := toolcallctx.ToolCallFromContext(ctx); ok {
		return tc.ID
	}
	return ""
}

// approvalGatedToolAdapter wraps one already-converted sdktools.Tool
// with a synchronous approval gate. It is the SDK-path twin of
// internal/agent/loop_tool_exec.go's gate check: for any tool whose
// capability.Class >= tools.ExecutionWrite, it publishes an
// EventToolPending (when EmitPending is non-nil), invokes the gate
// synchronously, and either delegates to the inner tool or returns
// the denial Err as the RoleTool content.
//
// Layering: this wrapper sits OUTSIDE admissionCheckedToolAdapter
// (which already runs first inside the wrapped inner tool), so a
// staged or unadmitted tool never reaches the approval gate.
//
// Standing decisions ("always approve" / "always deny") are
// consulted BEFORE the gate so a standing verdict short-circuits
// the prompt for the rest of the session. The same *ApprovalStanding
// instance backs the legacy path; passing the same pointer across
// paths is what lets a "always" decision persist across legacy and
// SDK turns within one session.
type approvalGatedToolAdapter struct {
	inner           sdktools.Tool
	cliName         string
	gate            func(ctx context.Context, name string, args json.RawMessage) ApprovalResult
	standing        *ApprovalStanding
	policy          string
	emitPending     func(toolCallID, name, detail, input string)
	recordDenied    func(toolCallID, name, reason string)
	getCapabilities func(json.RawMessage) tools.Capability
}

// Compile-time assertions: the adapter satisfies the SDK interfaces.
var _ sdktools.Tool = (*approvalGatedToolAdapter)(nil)
var _ sdktools.SchemaTool = (*approvalGatedToolAdapter)(nil)
var _ sdktools.ProfiledTool = (*approvalGatedToolAdapter)(nil)

func (a *approvalGatedToolAdapter) Name() string { return a.cliName }

// ExecutionProfile forwards the inner tool's profile so the SDK's
// run-timeout resolver, which consults only the outermost registered
// value, still sees the CLI tool's declared Capability.Timeout through
// the approval layer.
func (a *approvalGatedToolAdapter) ExecutionProfile() sdktools.ExecutionProfile {
	return sdktools.ExecutionProfileOf(a.inner)
}

// ParameterSchema forwards to the inner schema-publishing tool when
// the underlying adapter is a SchemaTool; the SDK's Definitions helper
// requires SchemaTool to be implemented when the registry is non-empty.
func (a *approvalGatedToolAdapter) ParameterSchema() []byte {
	if s, ok := a.inner.(sdktools.SchemaTool); ok {
		return s.ParameterSchema()
	}
	return nil
}

func (a *approvalGatedToolAdapter) DecodeArguments(raw []byte) (sdktools.InOut, error) {
	if d, ok := a.inner.(interface {
		DecodeArguments([]byte) (sdktools.InOut, error)
	}); ok {
		return d.DecodeArguments(raw)
	}
	if !json.Valid(raw) {
		return sdktools.InOut{}, fmt.Errorf("sdkadapter: tool %q: arguments are not valid JSON", a.cliName)
	}
	return sdktools.InOut{Value: json.RawMessage(raw)}, nil
}

// IsAutoApproval reports whether the policy represents auto-approval.
func IsAutoApproval(policy string) bool {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case "auto", "never", "none", "yolo":
		return true
	default:
		return false
	}
}

// IsAlwaysApproval reports whether the policy represents always-prompt.
func IsAlwaysApproval(policy string) bool {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case "always", "paranoid", "all":
		return true
	default:
		return false
	}
}

// IsDenyApproval reports whether the policy represents auto-deny: every
// gated tool call is rejected without a prompt (the "deny" settings-screen
// choice, config.ApprovalPolicyDeny).
func IsDenyApproval(policy string) bool {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case "deny", "deny_always", "never-approve":
		return true
	default:
		return false
	}
}

func (a *approvalGatedToolAdapter) Run(ctx context.Context, in sdktools.InOut) (sdktools.Out, error) {
	if IsAutoApproval(a.policy) {
		return a.inner.Run(ctx, in)
	}
	args := json.RawMessage(nil)
	if v, ok := in.Value.(json.RawMessage); ok {
		args = v
	} else if b, err := json.Marshal(in.Value); err == nil {
		args = b
	}
	// Read-class and Unclassified tools bypass the gate unless policy is "always".
	// Mirror the legacy executeToolTask threshold.
	cap := a.getCapabilities(args)
	if !IsAlwaysApproval(a.policy) && !IsDenyApproval(a.policy) && cap.Class < tools.ExecutionWrite {
		return a.inner.Run(ctx, in)
	}
	// Deny policy short-circuits the gate the same way auto-approval does,
	// just in the opposite direction: no pending prompt is ever emitted.
	if IsDenyApproval(a.policy) {
		return a.denied(ctx, "auto-denied (approval policy is \"deny\")")
	}
	// Standing decisions short-circuit the gate.
	if a.standing != nil {
		if v, ok := a.standing.Lookup(a.cliName); ok {
			if !v {
				return a.denied(ctx, "standing decision")
			}
			return a.inner.Run(ctx, in)
		}
	}
	if a.emitPending != nil {
		// Emit the pending advisory BEFORE invoking the gate so the UI
		// can render the prompt while the gate blocks. The bridge
		// downstream reconstructs an agent.EventToolPending and routes it
		// through the same emit path the legacy loop uses. toolCallID is
		// the in-flight call id (toolcallctx.ToolCall.ID) so the UI's
		// approval resolver can match a user decision back to this gate;
		// without it, the gate blocks forever after approval.
		a.emitPending(callIDFromContext(ctx), a.cliName, approvalClassName(cap.Class), string(args))
	}
	if a.gate == nil {
		// A call that needs approval with nobody to ask is denied, never run.
		// This is reachable only when a deny policy built this adapter without
		// a gate, but the direction matters more than the reachability: the
		// absence of an approver must never read as approval.
		return a.denied(ctx, "no approver is attached to this session")
	}
	res := a.gate(ctx, a.cliName, args)
	if res.ApprovedForClass && a.standing != nil {
		if res.Approved {
			a.standing.Allow(a.cliName, cap.Class)
		} else {
			a.standing.Deny(a.cliName, cap.Class)
		}
	}
	if !res.Approved {
		errText := res.Err
		if errText == "" {
			errText = "denied"
		}
		return a.denied(ctx, errText)
	}
	return a.inner.Run(ctx, in)
}

// capabilitiesFor returns a closure that exposes one CLI tool's
// Capability(args) at call time. Tools that implement CapableTool get
// the real per-args capability; tools that do not get ExecutionExternal
// (the conservative default the registry already applies in
// tools.Registry.Capability).
func capabilitiesFor(t tools.Tool) func(json.RawMessage) tools.Capability {
	if capable, ok := t.(tools.CapableTool); ok {
		return func(args json.RawMessage) tools.Capability {
			return capable.Capability(args)
		}
	}
	return func(json.RawMessage) tools.Capability {
		return tools.Capability{Class: tools.ExecutionExternal}
	}
}

// approvalClassName is a duplicate of the legacy helper. The SDK path
// cannot import the agent package's unexported helper, so the same
// stable mapping is reproduced here. If a tools.ExecutionClass.String()
// method lands upstream, this becomes a one-liner.
func approvalClassName(c tools.ExecutionClass) string {
	switch c {
	case tools.ExecutionRead:
		return "read"
	case tools.ExecutionWrite:
		return "write"
	case tools.ExecutionExternal:
		return "external"
	default:
		return "unclassified"
	}
}

// denied returns the model-visible refusal AND reports the refusal to the
// operator's surfaces.
//
// The two halves used to disagree. The model was told the call was denied,
// while every viewer was told it completed: a denial returns here without
// entering the dispatcher shim, so no outcome is recorded for the call, and
// the loop's no-outcome fallback emits a tool_end reading "completed
// (duplicate)" - which both the NDJSON status mapping and the TUI's own OK
// computation read as success.
//
// That is the worst direction for this particular error to point. An operator
// refuses a command and the transcript, local and remote, says it ran.
func (a *approvalGatedToolAdapter) denied(ctx context.Context, reason string) (sdktools.Out, error) {
	if a.recordDenied != nil {
		a.recordDenied(callIDFromContext(ctx), a.cliName, reason)
	}
	return sdktools.Out{Value: fmt.Sprintf("tool call denied by user: %s", reason)}, nil
}
