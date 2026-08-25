package uiadapter_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// The shipped TUI arms its approval prompt from the tool.pending uievent on
// TurnHandle.Events() and resolves by ToolCallID. Nothing drains
// Approver.Pending() in live mode. The gate must therefore never block on the
// Pending() publish: with a blocking send, the buffer fills after
// defaultPendingBuffer gated calls in one session and EVERY later gated tool
// call hangs forever, even though the user resolves each prompt.
func TestApprover_GateNeverBlocksWithoutPendingConsumer(t *testing.T) {
	sess := chat.NewSession(&config.Resolved{}, nil)
	appr := uiadapter.NewApprover(sess)

	// Deliberately never read appr.Pending(). Run well past the channel
	// buffer; each gate call is resolved the way the TUI does, by ID.
	const calls = 40
	for i := 1; i <= calls; i++ {
		done := make(chan struct{})
		go func() {
			// No toolcallctx on the context, so the gate falls back to the
			// generated "appr-N" id, which is deterministic per Approver.
			res := sess.ApprovalGate(context.Background(), "write_file", json.RawMessage(`{}`))
			if !res.Approved {
				t.Errorf("call %d: expected approval, got %+v", i, res)
			}
			close(done)
		}()

		id := fmt.Sprintf("appr-%d", i)
		deadline := time.After(2 * time.Second)
	resolve:
		for {
			// Resolve retries until the gate has registered its waiter; a
			// pre-registration Resolve is a documented no-op.
			appr.Resolve(id, ports.DecisionOnce)
			select {
			case <-done:
				break resolve
			case <-deadline:
				t.Fatalf("gate call %d blocked: Pending() publish must not block when nothing drains it", i)
			case <-time.After(5 * time.Millisecond):
			}
		}
	}
	_ = appr
}
