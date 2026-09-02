package uiadapter

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
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
