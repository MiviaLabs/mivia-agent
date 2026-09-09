package sdkadapter

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/tools"
	sdkagentloop "github.com/MiviaLabs/mivia-ai-sdk/agentloop"
	sdktools "github.com/MiviaLabs/mivia-ai-sdk/tools"
)

// callIDFromContext returns the in-flight call's id from toolcallctx, or ""
// when the ctx did not carry one (an SDK call outside the loop, or a
// hand-built test fixture). The pending event must carry the id so the UI's
// approval resolver can match a decision back to the gate blocked on it, and
// uiadapter.approvalKey derives the waiting map's key the same way - the two
// have to agree or the prompt can never be answered.
//
// No name fallback here, deliberately: the name is not a per-call identity,
// and uiadapter cannot key its waiting map on it without cross-wiring two
// overlapping calls to the same tool. See uiadapter.approvalKey.
func callIDFromContext(ctx context.Context) string {
	if tc, ok := sdkagentloop.ToolCallFromContext(ctx); ok {
		return tc.ID
	}
	return ""
}

// recordKeyFromContext returns the key an OUTCOME is recorded under: the
// call's id, else its name. This is the rule internal/agent's toolCallKey
// applies when it later looks the outcome up, so a denial recorded under a
// blank id is one bridgeToolCallEnd cannot find - the refusal then reaches the
// operator as a bare "failed" with no reason.
//
// Unlike the prompt key above, this one has no uniqueness requirement to
// violate: it is a report about a call that is already over, and the agent
// loop keys its own outcome map the same way, so agreeing with it is the whole
// job.
//
// It is a copy of internal/agent's toolCallKey, kept in step by contract. Not
// because an import is impossible - internal/agent imports this package, so a
// shared helper could live here - but because the two answer different
// questions that happen to share a rule today: this one must match whatever
// the loop's recorder looks up, and it should follow that recorder if it ever
// changes, not constrain it. If a third caller needs the same rule, export it
// from here rather than writing a fourth copy.
func recordKeyFromContext(ctx context.Context) string {
	tc, ok := sdkagentloop.ToolCallFromContext(ctx)
	if !ok {
		return ""
	}
	if tc.ID != "" {
		return tc.ID
	}
	return tc.Name
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

	capability := a.getCapabilities(args)

	// The decision itself lives in DecideApproval, because this wrapper is not
	// the only route to executing a tool and the routes that bypassed it had
	// no approval at all.
	decision := DecideApproval(ctx, ApprovalDeps{
		Policy:      a.policy,
		Standing:    a.standing,
		Gate:        a.gate,
		EmitPending: a.emitPending,
	}, ApprovalRequest{
		ToolCallID:  callIDFromContext(ctx),
		Name:        a.cliName,
		Class:       capability.Class,
		ResourceKey: capability.ResourceKey,
		Args:        args,
	})
	if !decision.Approved {
		return a.denied(ctx, decision.Reason)
	}
	return a.inner.Run(ctx, in)
}

// capabilitiesFor returns the class lookup for one CLI tool. A tool that
// declares no capability is treated as ExecutionExternal - the most
// restrictive class - so an unclassified tool is gated rather than waved
// through.
func capabilitiesFor(t tools.Tool) func(json.RawMessage) tools.Capability {
	// One implementation of the unclassified default, in tools. This was a
	// second copy of the same rule and the deferred path in internal/chat was
	// a third; a review found one of them unpinned and the three free to
	// diverge.
	return func(args json.RawMessage) tools.Capability {
		return tools.CapabilityOf(t, args)
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
// the loop's no-outcome fallback emitted a tool_end reading "completed
// (duplicate)" - which both the NDJSON status mapping and the TUI's own OK
// computation read as success. An operator refused a command and the
// transcript, local and remote, said it ran.
//
// That fallback now reads "failed", so the worst direction is closed even
// when this recorder is skipped - but it can only say THAT a call failed,
// never why, so recording the denial here is still what carries the reason.
func (a *approvalGatedToolAdapter) denied(ctx context.Context, reason string) (sdktools.Out, error) {
	if a.recordDenied != nil {
		a.recordDenied(recordKeyFromContext(ctx), a.cliName, reason)
	}
	return sdktools.Out{Value: fmt.Sprintf("tool call denied by user: %s", reason)}, nil
}

// MaxResultBytes and Privileged forward the inner tool's declared
// budget and privilege marker, so the approval wrapper never masks
// the capabilities the loop's shaping and scope checks read.
func (a *approvalGatedToolAdapter) MaxResultBytes() int {
	n, _ := sdktools.ResultBudgetOf(a.inner)
	return n
}

func (a *approvalGatedToolAdapter) Privileged() bool {
	return sdktools.IsPrivileged(a.inner)
}
