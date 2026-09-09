package uiadapter

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// /new and /resume hand-copy runtime state from an existing pool member -
// tools, event bus, context store, redaction policy - and carried none of the
// approval state. A threat model measured it: a session started under "deny"
// with a live approver produced, after /new, policy="" and gate=nil. The
// operator's most restrictive setting evaporated on a keystroke that looks
// like housekeeping.

// contextBoundSession mirrors the helper in the external test package, which
// this file cannot reach: these tests need the unexported session behind a
// Conversation, so they live in the internal package.
func contextBoundSession(t *testing.T, res *config.Resolved, store *storage.SQLite, sessionID string) *chat.Session {
	t.Helper()
	sess := chat.NewSession(res, nil)
	sess.SessionID = sessionID
	principal, err := contextstate.NewPrincipal("workspace", sess.SessionID, "subject")
	if err != nil {
		t.Fatal(err)
	}
	manager := &contextmgr.ContextManager{
		PreparationManager:  contextmgr.StructuralPreparationManager{},
		CheckpointPublisher: contextmgr.PreparationCommitter{Store: store},
		Enabled:             true,
	}
	if err := sess.SetContextManager(manager, principal); err != nil {
		t.Fatal(err)
	}
	if err := sess.SetContextStore(store); err != nil {
		t.Fatal(err)
	}
	return sess
}

func approvalTestStore(t *testing.T) *storage.SQLite {
	t.Helper()
	store, err := storage.OpenSQLite(t.TempDir() + "/context.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

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
//
// This used to Skip when the pool could not Load, which meant the /resume half
// of the fix was shipped unverified: a review deleted the inherit call from
// GetOrCreate alone and the package stayed green. It now builds a real
// context-backed session so the load path actually runs.
func TestAResumedSessionKeepsTheApprovalPolicy(t *testing.T) {
	// A provider binding is required before a session can be saved, so this
	// fixture carries one; the rest of the tests here never persist.
	res := &config.Resolved{
		ProviderName: "fake",
		Model:        "model",
		Approvals:    config.ApprovalsConfig{DefaultMode: "deny"},
	}
	store := approvalTestStore(t)

	first := contextBoundSession(t, res, store, "first-session")
	first.SetBaseApprovalPolicy(config.ApprovalPolicyDeny)
	first.SetApprovalPolicy(config.ApprovalPolicyDeny)
	first.ApprovalGate = func(context.Context, string, json.RawMessage) sdkadapter.ApprovalResult {
		return sdkadapter.ApprovalResult{Approved: true}
	}

	// A saved session for the pool to resume, distinct from the pool member.
	saved := contextBoundSession(t, res, store, "resumed-session")
	if err := saved.Save("resumed-session"); err != nil {
		t.Skipf("this fixture cannot save a session: %v", err)
	}

	pool := NewSessionPool(first, res, nil, false)
	conv, err := pool.GetOrCreate("resumed-session")
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	resumed := conv.(*Conversation).sess
	if resumed == first {
		t.Fatal("the pool returned the existing session; the resume path never ran")
	}

	if got := resumed.ApprovalPolicyValue(); got != config.ApprovalPolicyDeny {
		t.Errorf("policy = %q, want deny: /resume discarded the operator's "+
			"configured approval policy", got)
	}
	if resumed.ApprovalGate == nil {
		t.Error("the resumed session has no approval gate, so nothing can ask the " +
			"operator")
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

	// The cache must EXIST. A nil one made this test pass for the wrong
	// reason, and made the affordance dead rather than fresh: DecideApproval
	// guards every standing read and write on a non-nil cache, so "a always"
	// would be accepted by the prompt and silently forgotten.
	if fresh.ApprovalStanding == nil {
		t.Fatal("the fresh session has no standing cache, so \"always allow\" is " +
			"discarded in every conversation after the first")
	}
	if _, ok := fresh.ApprovalStanding.Lookup(sdkadapter.StandingKey{Name: "run_command"}); ok {
		t.Error("a standing \"always allow\" crossed into a new conversation")
	}

	// And it must be usable: a decision recorded here is remembered here.
	own := sdkadapter.StandingKey{Name: "edit_file", Class: tools.ExecutionWrite, ResourceKey: "/repo/a.txt"}
	fresh.ApprovalStanding.Allow(own)
	if approved, ok := fresh.ApprovalStanding.Lookup(own); !ok || !approved {
		t.Error("a standing decision made in the fresh session was not remembered")
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
