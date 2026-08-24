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
	"github.com/MiviaLabs/mivia-ai-sdk/toolcallctx"
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

// gate is installed as chat.Session.ApprovalGate.
func (a *Approver) gate(ctx context.Context, name string, args json.RawMessage) sdkadapter.ApprovalResult {
	if a.sess != nil && (a.sess.ApprovalPolicy == "auto" || a.sess.ApprovalPolicy == "never" || a.sess.ApprovalPolicy == "yolo") {
		return sdkadapter.ApprovalResult{Approved: true}
	}
	// The waiting map's key must be the id the UI will Resolve with.
	// The new TUI arms its approval prompt from the tool.pending
	// uievent, whose ToolCallID is the in-flight TOOL CALL id
	// (EventToolPending.ToolCallID, stamped from toolcallctx by the
	// SDK approval wrapper). Keying by an internally generated
	// "appr-N" id made every Resolve a silent no-op and the gate
	// blocked forever - the "approved but still pending" hang. When
	// the ctx carries no tool call (legacy backend, direct callers),
	// fall back to the generated id; consumers of Pending() resolve
	// with whatever ID the request carries, so both domains stay
	// self-consistent.
	callID := ""
	if tc, ok := toolcallctx.ToolCallFromContext(ctx); ok && tc.ID != "" {
		callID = tc.ID
	}
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

	select {
	case a.pendingCh <- req:
	case <-ctx.Done():
		a.mu.Lock()
		delete(a.waiting, callID)
		a.mu.Unlock()
		return sdkadapter.ApprovalResult{
			Approved: false,
			Err:      "canceled",
		}
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
