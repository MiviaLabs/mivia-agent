package uiadapter

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// /new and /resume hand-copy runtime state from an existing pool member -
// tools, event bus, context store, redaction policy - and carried none of the
// approval state. A threat model measured it: a session started under "deny"
// with a live approver produced, after /new, policy="" and gate=nil. The
// operator's most restrictive setting evaporated on a keystroke that looks
// like housekeeping.

func denyingConfig() *config.Resolved {
	return &config.Resolved{Approvals: config.ApprovalsConfig{DefaultMode: "deny"}}
}

// poolWithApprover builds a pool whose first session has a live gate, the
// shape a real TUI is in.
func poolWithApprover(t *testing.T, res *config.Resolved) (*SessionPool, *chat.Session) {
	t.Helper()
	first := chat.NewSession(res, nil)
	first.SetBaseApprovalPolicy("deny")
	first.SetApprovalPolicy("deny")
	first.ApprovalGate = func(context.Context, string, json.RawMessage) sdkadapter.ApprovalResult {
		return sdkadapter.ApprovalResult{Approved: true}
	}
	return NewSessionPool(first, res, nil, false), first
}

// TestAFreshSessionKeepsTheApprovalPolicy is the reproduction: /new.
func TestAFreshSessionKeepsTheApprovalPolicy(t *testing.T) {
	pool, _ := poolWithApprover(t, denyingConfig())

	conv, err := pool.CreateFresh()
	if err != nil {
		t.Fatalf("CreateFresh: %v", err)
	}
	fresh := conv.(*Conversation).sess

	if got := fresh.ApprovalPolicyValue(); got != config.ApprovalPolicyDeny {
		t.Errorf("policy = %q, want deny: /new discarded the operator's configured "+
			"approval policy, so the next conversation runs write tools unprompted",
			got)
	}
	if fresh.ApprovalGate == nil {
		t.Error("the fresh session has no approval gate, so nothing can ask the " +
			"operator and the approver the UI is watching is never reached")
	}
}

// TestAResumedSessionKeepsTheApprovalPolicy is the same defect on /resume.
func TestAResumedSessionKeepsTheApprovalPolicy(t *testing.T) {
	pool, first := poolWithApprover(t, denyingConfig())

	conv, err := pool.GetOrCreate("some-other-session")
	if err != nil {
		// Load can fail with no store; the wiring is still what this asserts.
		t.Skipf("GetOrCreate needs a session store: %v", err)
	}
	resumed := conv.(*Conversation).sess
	if resumed == first {
		t.Skip("the pool returned the existing session; nothing new was built")
	}

	if got := resumed.ApprovalPolicyValue(); got != config.ApprovalPolicyDeny {
		t.Errorf("policy = %q, want deny on a resumed session", got)
	}
	if resumed.ApprovalGate == nil {
		t.Error("a resumed session has no approval gate")
	}
}

// TestAFreshSessionDoesNotInheritATransientYolo holds the direction that makes
// this a security fix rather than a copy. /yolo is a deliberate, temporary
// loosening of ONE conversation; carrying it into a brand-new one would extend
// a bypass the operator never asked to extend.
func TestAFreshSessionDoesNotInheritATransientYolo(t *testing.T) {
	pool, first := poolWithApprover(t, denyingConfig())

	// The operator toggles yolo on the first conversation.
	first.SetApprovalPolicy(config.ApprovalPolicyAuto)

	conv, err := pool.CreateFresh()
	if err != nil {
		t.Fatalf("CreateFresh: %v", err)
	}
	fresh := conv.(*Conversation).sess

	if config.IsAutoPolicy(fresh.ApprovalPolicyValue()) {
		t.Error("a new conversation started in yolo because a sibling was in yolo; " +
			"a temporary bypass must not become the next conversation's posture")
	}
}

// TestAFreshSessionDoesNotInheritStandingDecisions: "always allow this tool" is
// a decision about one conversation. Carrying it across /new widens it to a
// conversation the operator has not seen.
func TestAFreshSessionDoesNotInheritStandingDecisions(t *testing.T) {
	pool, first := poolWithApprover(t, denyingConfig())
	first.ApprovalStanding = sdkadapter.NewApprovalStanding()
	first.ApprovalStanding.Allow(sdkadapter.StandingKey{Name: "run_command"})

	conv, err := pool.CreateFresh()
	if err != nil {
		t.Fatalf("CreateFresh: %v", err)
	}
	fresh := conv.(*Conversation).sess

	if fresh.ApprovalStanding != nil {
		if _, ok := fresh.ApprovalStanding.Lookup(sdkadapter.StandingKey{Name: "run_command"}); ok {
			t.Error("a standing \"always allow\" crossed into a new conversation")
		}
	}
}

// TestTheFirstSessionOfAnEmptyPoolStillGetsThePolicy covers the case my own
// first attempt missed: with no sibling to copy from, the loop never runs, and
// the configured policy has to be applied anyway.
func TestTheFirstSessionOfAnEmptyPoolStillGetsThePolicy(t *testing.T) {
	res := denyingConfig()
	pool := NewSessionPool(nil, res, nil, false)

	conv, err := pool.CreateFresh()
	if err != nil {
		t.Fatalf("CreateFresh: %v", err)
	}
	fresh := conv.(*Conversation).sess

	if got := fresh.ApprovalPolicyValue(); got != config.ApprovalPolicyDeny {
		t.Errorf("policy = %q, want deny: a session built with no sibling to inherit "+
			"from still has an operator with a configured policy", got)
	}
}

// TestAnInheritedGateAnswersUnderTheCallersPolicy is the leak that survived
// the first fix.
//
// /new carries the gate over so the UI has one place to render prompts from,
// and that gate is a method bound to the session it was CONSTRUCTED against.
// It short-circuits on that session's live policy. So the fresh session's
// policy decided whether to ask, and the first session's answered - a /yolo on
// the first conversation auto-approved write tools in a fresh one whose own
// policy said to prompt.
//
// The earlier test asserted only on the fresh session's policy FIELD, which
// was already correct. This one drives a decision.
func TestAnInheritedGateAnswersUnderTheCallersPolicy(t *testing.T) {
	res := &config.Resolved{Approvals: config.ApprovalsConfig{DefaultMode: "once"}}

	first := chat.NewSession(res, nil)
	first.SetBaseApprovalPolicy(config.ApprovalPolicyWriteOnly)
	first.SetApprovalPolicy(config.ApprovalPolicyWriteOnly)
	approver := NewApprover(first)
	pool := NewSessionPool(first, res, nil, false)

	// The operator toggles yolo on the FIRST conversation only.
	first.SetApprovalPolicy(config.ApprovalPolicyAuto)

	conv, err := pool.CreateFresh()
	if err != nil {
		t.Fatalf("CreateFresh: %v", err)
	}
	fresh := conv.(*Conversation).sess
	if fresh.ApprovalGate == nil {
		t.Fatal("the fresh session has no gate; this test proves nothing")
	}
	if config.IsAutoPolicy(fresh.ApprovalPolicyValue()) {
		t.Fatal("the fresh session inherited yolo; a different defect")
	}

	// Ask the way production does: through the one decision function, under
	// the FRESH session's policy. Nothing will answer the prompt, so a
	// correctly-behaving gate must block rather than return an approval.
	done := make(chan sdkadapter.ApprovalDecision, 1)
	go func() {
		done <- sdkadapter.DecideApproval(context.Background(), sdkadapter.ApprovalDeps{
			Policy: fresh.ApprovalPolicyValue(),
			Gate:   fresh.ApprovalGate,
		}, sdkadapter.ApprovalRequest{
			Name: "run_command", Class: tools.ExecutionExternal,
			Args: json.RawMessage(`{"command":"rm -rf /"}`),
		})
	}()

	select {
	case got := <-done:
		if got.Approved {
			t.Fatal("a write tool was approved with no prompt: the inherited gate " +
				"answered from the first conversation's transient yolo, in a session " +
				"whose own policy says to ask")
		}
	case <-time.After(300 * time.Millisecond):
		// Blocked waiting for an operator, which is the correct behaviour.
	}
	_ = approver
}
