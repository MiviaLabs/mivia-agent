package uiadapter

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	sdkagentloop "github.com/MiviaLabs/mivia-ai-sdk/agentloop"
)

const defaultPendingBuffer = 16

// Approver implements ports.Approver and bridges tool approval requests
// between the chat.Session's ApprovalGate and the UI's Pending / Resolve surface.
type Approver struct {
	sess      *chat.Session
	pendingCh chan ports.ApprovalRequest

	mu      sync.Mutex
	waiting map[string]chan ports.Decision
	counter uint64
}

// Compile-time check that Approver implements ports.Approver.
var _ ports.Approver = (*Approver)(nil)

// NewApprover creates an Approver and hooks it into sess.ApprovalGate.
// If sess.ApprovalStanding is nil, it initializes a new standing cache so
// session-level always decisions persist.
func NewApprover(sess *chat.Session) *Approver {
	a := &Approver{
		sess:      sess,
		pendingCh: make(chan ports.ApprovalRequest, defaultPendingBuffer),
		waiting:   make(map[string]chan ports.Decision),
	}
	if sess != nil {
		if sess.ApprovalStanding == nil {
			sess.ApprovalStanding = sdkadapter.NewApprovalStanding()
		}
		sess.ApprovalGate = a.gate
	}
	return a
}

// Pending returns the read-only channel delivering approval requests to the UI.
func (a *Approver) Pending() <-chan ports.ApprovalRequest {
	return a.pendingCh
}

// Resolve answers a pending approval request by ID with the user's decision.
// Resolving an unknown or already resolved ID is a safe no-op.
func (a *Approver) Resolve(id string, decision ports.Decision) {
	a.mu.Lock()
	ch, ok := a.waiting[id]
	if ok {
		delete(a.waiting, id)
	}
	a.mu.Unlock()

	if !ok {
		return
	}

	select {
	case ch <- decision:
	default:
	}
}

// standingPolicyResult reports the session's persistent auto-approve/
// auto-deny policy verdict, if any, so gate can short-circuit before ever
// arming a prompt. The second return is false when the policy is neither
// (i.e. "once"/write-only), meaning gate must fall through to the
// interactive prompt below.
func (a *Approver) standingPolicyResult(ctx context.Context) (sdkadapter.ApprovalResult, bool) {
	// The CALLER's policy wins. One approver now serves several sessions -
	// /new inherits this gate so the UI has a single place to render prompts
	// from - and a.sess is whichever session it was constructed against. A
	// transient /yolo on that one used to auto-approve write tools in a fresh
	// conversation whose own policy said to prompt.
	policy, ok := sdkadapter.ApprovalPolicyFromContext(ctx)
	if !ok {
		if a.sess == nil {
			return sdkadapter.ApprovalResult{}, false
		}
		policy = a.sess.ApprovalPolicyValue()
	}
	if sdkadapter.IsAutoApproval(policy) {
		return sdkadapter.ApprovalResult{Approved: true}, true
	}
	// "deny" policy denies every gated call without ever arming a prompt,
	// symmetric with the "always approve" short-circuit above. The SDK
	// tool-registry path already short-circuits before reaching this gate
	// (internal/sdkadapter/approver.go's approvalGatedToolAdapter.Run), but
	// direct/legacy callers of ApprovalGate must see the same behavior.
	if sdkadapter.IsDenyApproval(policy) {
		return sdkadapter.ApprovalResult{Approved: false, Err: "auto-denied (approval policy is \"deny\")"}, true
	}
	return sdkadapter.ApprovalResult{}, false
}

// approvalKey is the key the waiting map must use: the same one the SDK
// approval wrapper publishes the prompt under, because the TUI arms its prompt
// from that uievent and Resolves with that id. Keying by an internally
// generated "appr-N" while the prompt announced the call id made every Resolve
// a silent no-op and the gate block forever - the "approved but still pending"
// hang.
//
// The TOOL CALL id only. NOT the name as a fallback, even though an ID-less
// call (a provider stream sending the name delta before, or without, the id)
// then gets a generated id here that the published prompt cannot match, and
// blocks until its context dies. A name is not a per-call identity: two
// overlapping calls to one tool - ordinary for parallel subagents, which all
// share this Approver - would collide on it. The second registration would
// overwrite the first's channel, the operator would be shown one prompt and
// asked to decide once, and that decision would authorize the OTHER call,
// unseen. A hang is a bad outcome; approving a command nobody was shown is a
// worse one, so this half of the ID-less case stays unfixed until there is a
// per-call identity to key it on. See sdkadapter.recordKeyFromContext for the
// reporting half, which has no such constraint.
//
// The operator is still SHOWN that prompt (the transcript renders a pending
// block with a blank CallID), so approving it appears to do nothing until the
// tool's own timeout returns canceled. That is worth knowing before debugging
// it: the button is not broken, the call has no id to answer under.
//
// Returns "" when the ctx carries no tool call at all (legacy backend, direct
// callers); the caller then generates an id, and consumers of Pending()
// resolve with whatever ID the request carries - that path works, because the
// same id is published and registered.
func approvalKey(ctx context.Context) string {
	tc, ok := sdkagentloop.ToolCallFromContext(ctx)
	if !ok {
		return ""
	}
	return tc.ID
}

// gate is installed as chat.Session.ApprovalGate.
func (a *Approver) gate(ctx context.Context, name string, args json.RawMessage) sdkadapter.ApprovalResult {
	if res, short := a.standingPolicyResult(ctx); short {
		return res
	}
	callID := approvalKey(ctx)
	if callID == "" {
		callID = fmt.Sprintf("appr-%d", atomic.AddUint64(&a.counter, 1))
	}

	var parsedArgs map[string]any
	if len(args) > 0 {
		_ = json.Unmarshal(args, &parsedArgs)
	}

	ch := make(chan ports.Decision, 1)
	a.mu.Lock()
	a.waiting[callID] = ch
	a.mu.Unlock()

	req := ports.ApprovalRequest{
		ID:       callID,
		ToolName: name,
		Args:     parsedArgs,
	}

	// The publish must NEVER block: the shipped TUI arms its approval prompt
	// from the tool.pending uievent on TurnHandle.Events() and resolves by
	// ToolCallID - nothing drains Pending() in live mode. A blocking send
	// filled the buffer after defaultPendingBuffer gated calls in one session
	// and every later gated tool call hung here forever, stuck BEFORE the
	// decision select, so even a correct Resolve could not unblock it. A full
	// buffer drops the request; the uievent stream remains the prompt's
	// source of truth and the decision wait below still honors ctx.Done().
	select {
	case a.pendingCh <- req:
	default:
	}

	select {
	case d := <-ch:
		switch d {
		case ports.DecisionOnce:
			return sdkadapter.ApprovalResult{Approved: true}
		case ports.DecisionAlways:
			return sdkadapter.ApprovalResult{Approved: true, ApprovedForClass: true}
		case ports.DecisionDeny:
			return sdkadapter.ApprovalResult{Approved: false, Err: "tool call denied by user"}
		case ports.DecisionDenyAlways:
			return sdkadapter.ApprovalResult{Approved: false, ApprovedForClass: true, Err: "tool call denied by user"}
		default:
			return sdkadapter.ApprovalResult{Approved: false, Err: "unknown decision"}
		}
	case <-ctx.Done():
		a.mu.Lock()
		delete(a.waiting, callID)
		a.mu.Unlock()
		return sdkadapter.ApprovalResult{
			Approved: false,
			Err:      "canceled",
		}
	}
}
