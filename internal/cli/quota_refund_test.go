package cli

// E1 (confirmed defect): quota is consumed BEFORE persist. A failed
// PostTaskMessage must refund the burned slot so a transient persist failure
// never permanently consumes a message-budget slot (messageQuota is otherwise
// increment-only). These tests pin the refund on every postMessageTool path:
// finding, question, and ask.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

// TestQuotaRefundedWhenFindingPostFails: a finding whose PostTaskMessage fails
// after ConsumeMessageQuota must refund the slot — the task's budget count is
// unchanged, so a subsequent ConsumeMessageQuota at the same ceiling succeeds.
func TestQuotaRefundedWhenFindingPostFails(t *testing.T) {
	cfg := config.DefaultSubagentConfig
	cfg.Messaging.MaxMessagesPerTask = 2
	tool, c, _, runID, _, _ := setupPostMessageEnv(t, cfg)
	// Identity points at a task that does not exist in the ledger →
	// PostTaskMessage fails after ConsumeMessageQuota.
	ctx := runtime.ContextWithTaskIdentity(context.Background(), runtime.TaskIdentity{
		RunID: runID, TaskID: "no-such-task", Agent: "worker",
	})
	if _, err := tool.Execute(ctx, json.RawMessage(`{"kind":"finding","body":"x"}`)); err == nil {
		t.Fatal("expected post failure")
	}
	// The burned slot must have been refunded: a fresh consume at max=1 succeeds.
	if err := c.ConsumeMessageQuota(runID, "no-such-task", 1); err != nil {
		t.Fatalf("quota not refunded after failed finding persist: %v", err)
	}
}

// TestQuotaRefundedWhenQuestionPostFails: same refund on the parked-question
// path (waitForAnswer).
func TestQuotaRefundedWhenQuestionPostFails(t *testing.T) {
	cfg := config.DefaultSubagentConfig
	cfg.Messaging.MaxMessagesPerTask = 2
	tool, c, _, runID, _, _ := setupPostMessageEnv(t, cfg)
	ctx := runtime.ContextWithTaskIdentity(context.Background(), runtime.TaskIdentity{
		RunID: runID, TaskID: "no-such-task", Agent: "worker",
	})
	if _, err := tool.Execute(ctx, json.RawMessage(`{"kind":"question","body":"q","wait_seconds":1}`)); err == nil {
		t.Fatal("expected post failure")
	}
	if err := c.ConsumeMessageQuota(runID, "no-such-task", 1); err != nil {
		t.Fatalf("quota not refunded after failed question persist: %v", err)
	}
	// The failed question must also have unparked (no leaked park).
	if n := c.CountPendingQuestions(runID, "no-such-task"); n != 0 {
		t.Fatalf("park leaked after failed question persist: %d", n)
	}
}

// TestQuotaRefundedWhenAskPostFails: same refund on the parent-routed ask path
// (handleAsk).
func TestQuotaRefundedWhenAskPostFails(t *testing.T) {
	cfg := config.DefaultSubagentConfig
	cfg.Messaging.MaxMessagesPerTask = 2
	tool, c, _, runID, _, _ := setupPostMessageEnv(t, cfg)
	id := runtime.TaskIdentity{RunID: runID, TaskID: "no-such-task", Agent: "worker"}
	if _, err := tool.handleAsk(context.Background(), c, id, "q", nil, "peer", 1, ""); err == nil {
		t.Fatal("expected post failure")
	}
	if err := c.ConsumeMessageQuota(runID, "no-such-task", 1); err != nil {
		t.Fatalf("quota not refunded after failed ask persist: %v", err)
	}
}

// TestQuotaRefundedWhenAnswerPostFails: same refund on the peer-answer path
// (handlePeerAnswer). An ask is registered; the answering identity points at a
// task that does not exist in the ledger, so PostTaskMessage fails after
// ConsumeMessageQuota — the burned slot must be refunded, so a subsequent
// ConsumeMessageQuota at the same ceiling succeeds (with max_messages_per_task=1
// a missed refund would wedge the task permanently).
func TestQuotaRefundedWhenAnswerPostFails(t *testing.T) {
	cfg := config.DefaultSubagentConfig
	cfg.Messaging.MaxMessagesPerTask = 2
	tool, c, _, runID, taskID, ctx := setupPostMessageEnv(t, cfg)
	c.RegisterAsk(runID, taskID, "worker", "ask-refund", nil)
	// Identity points at a task that does not exist in the ledger →
	// PostTaskMessage fails after ConsumeMessageQuota.
	id := runtime.TaskIdentity{RunID: runID, TaskID: "no-such-task", Agent: "worker"}
	if _, err := tool.handlePeerAnswer(ctx, c, id, "body", "ask-refund"); err == nil {
		t.Fatal("expected post failure")
	}
	// The burned slot must have been refunded: a fresh consume at max=1 succeeds.
	if err := c.ConsumeMessageQuota(runID, "no-such-task", 1); err != nil {
		t.Fatalf("quota not refunded after failed answer persist: %v", err)
	}
}

// TestQuotaRefundDoesNotGrantCredit: refund floors at zero — a refund without a
// prior consume must not create negative credit that later bypasses the ceiling.
func TestQuotaRefundDoesNotGrantCredit(t *testing.T) {
	c, _ := newPostMessageCoordinatorFromCLI(t)
	c.RefundMessageQuota("r", "t")
	if err := c.ConsumeMessageQuota("r", "t", 0); err != nil {
		t.Fatal(err)
	}
	// At max=1 the first consume succeeds and the second fails: the stray
	// refund granted no extra credit.
	if err := c.ConsumeMessageQuota("r", "t", 1); err != nil {
		t.Fatalf("first consume after stray refund must succeed: %v", err)
	}
	if err := c.ConsumeMessageQuota("r", "t", 1); err == nil {
		t.Fatal("stray refund must not grant an extra slot")
	}
}
