package sdkadapter

import (
	"context"
	"encoding/json"

	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// ApprovalRequest is one call awaiting a decision.
type ApprovalRequest struct {
	// ToolCallID is the in-flight call id. The UI's resolver matches a
	// decision back to the blocked gate by it.
	ToolCallID string
	Name       string
	Class      tools.ExecutionClass
	Args       json.RawMessage
}

// ApprovalDecision is the answer, and the reason when it is a refusal.
type ApprovalDecision struct {
	Approved bool
	// Reason is empty when Approved. It is operator-facing text, never a
	// path or a secret.
	Reason string
}

// ApprovalDeps is what a decision needs. Every field may be nil or empty; the
// zero value denies anything that requires a decision, which is the direction
// this whole layer exists to guarantee.
type ApprovalDeps struct {
	Policy   string
	Standing *ApprovalStanding
	Gate     func(ctx context.Context, name string, args json.RawMessage) ApprovalResult
	// EmitPending announces the prompt before the gate blocks, so a UI can
	// render it while waiting. Optional.
	EmitPending func(toolCallID, name, detail, input string)
}

// DecideApproval answers whether one call may run.
//
// This is the ONE implementation of the policy. It was previously reachable
// only through the SDK tool wrapper, which meant every other route to
// executing a tool had no approval at all - and there are several. A threat
// model found a deferred-tool path invoking the runtime dispatcher directly
// and writing a file under a "deny" policy with a live approver attached.
//
// Re-implementing this decision at each such site is what the last three
// fixes in this area were about. So the decision moved here, and the callers
// ask instead of deciding.
func DecideApproval(ctx context.Context, deps ApprovalDeps, req ApprovalRequest) ApprovalDecision {
	if IsAutoApproval(deps.Policy) {
		return ApprovalDecision{Approved: true}
	}
	// An unset policy means this caller configured none. It is not a licence
	// to run: it is treated as auto only where the caller has opted out of
	// the layer entirely (see NeedsApprovalLayer). Reaching here with one
	// means a decision IS required.
	//
	// Read-class and unclassified calls bypass the prompt unless the policy
	// is "always" or "deny", matching the legacy threshold.
	if !IsAlwaysApproval(deps.Policy) && !IsDenyApproval(deps.Policy) && req.Class < tools.ExecutionWrite {
		return ApprovalDecision{Approved: true}
	}
	if IsDenyApproval(deps.Policy) {
		return ApprovalDecision{Reason: `auto-denied (approval policy is "deny")`}
	}
	if deps.Standing != nil {
		if v, ok := deps.Standing.Lookup(req.Name); ok {
			if !v {
				return ApprovalDecision{Reason: "standing decision"}
			}
			return ApprovalDecision{Approved: true}
		}
	}
	if deps.Gate == nil {
		// A call that needs approval with nobody to ask is denied, never run.
		// The absence of an approver must never read as approval.
		return ApprovalDecision{Reason: "no approver is attached to this session"}
	}
	if deps.EmitPending != nil {
		deps.EmitPending(req.ToolCallID, req.Name, approvalClassName(req.Class), string(req.Args))
	}
	res := deps.Gate(ctx, req.Name, req.Args)
	if res.ApprovedForClass && deps.Standing != nil {
		if res.Approved {
			deps.Standing.Allow(req.Name, req.Class)
		} else {
			deps.Standing.Deny(req.Name, req.Class)
		}
	}
	if res.Approved {
		return ApprovalDecision{Approved: true}
	}
	reason := res.Err
	if reason == "" {
		reason = "denied"
	}
	return ApprovalDecision{Reason: reason}
}
