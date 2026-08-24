package uiadapter_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
	"github.com/MiviaLabs/mivia-ai-sdk/toolcallctx"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// TestApprover_ResolveByToolCallIDFromContext pins the production
// wiring of the new TUI: the approval prompt is armed from the
// tool.pending uievent, whose ToolCallID is the in-flight TOOL CALL
// id (EventToolPending.ToolCallID, stamped from toolcallctx on the
// SDK path). The user's decision therefore arrives as
// Resolve("<tool call id>", ...). The gate MUST key its waiting map
// by that same id - keying by an internally generated "appr-N" id
// makes every Resolve a silent no-op and the gate blocks forever,
// which is exactly the "approved but still pending" hang.
func TestApprover_ResolveByToolCallIDFromContext(t *testing.T) {
	sess := chat.NewSession(&config.Resolved{}, nil)
	appr := uiadapter.NewApprover(sess)

	const callID = "call-write-42"
	// The SDK approval wrapper invokes the gate with the call's ctx,
	// which carries toolcallctx.
	gateCtx := toolcallctx.WithToolCall(context.Background(), sdkshape.ToolCall{
		ID: callID, Name: "write_file", Index: 0, Arguments: []byte(`{}`),
	})

	type result struct {
		res  interface{ Approved() bool }
		done chan struct{}
	}
	_ = result{}

	done := make(chan struct{})
	var approved bool
	go func() {
		defer close(done)
		res := sess.ApprovalGate(gateCtx, "write_file", json.RawMessage(`{"path":"/tmp/a"}`))
		approved = res.Approved
	}()

	// Give the gate a moment to register, then resolve by the TOOL
	// CALL id - the only id the UI knows.
	time.Sleep(50 * time.Millisecond)
	appr.Resolve(callID, ports.DecisionOnce)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("gate never unblocked: Resolve by tool call id did not reach the waiting map (approval hang)")
	}
	if !approved {
		t.Fatal("gate returned not-approved; decision lost in translation")
	}
}
