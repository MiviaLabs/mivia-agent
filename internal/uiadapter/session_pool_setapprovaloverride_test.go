package uiadapter

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
)

// TestSetApprovalOverrideSurvivesInheritApprovalClobber pins chunk 6's
// load-bearing ordering fact: CreateFreshInDir's own wireEntryLocked
// unconditionally overwrites a freshly spawned session's
// ApprovalGate/ApprovalPolicy via inheritApprovalLocked (D8's ambient
// posture inheritance), so SetApprovalOverride must be applied strictly
// AFTER CreateFreshInDir returns to survive. This spawns a session that
// inherits a SIBLING's gate/policy (via poolWithApprover's "deny" +
// live gate fixture), then calls SetApprovalOverride with a DIFFERENT
// gate/policy, and asserts the final wiring is the override, not the
// inherited sibling's.
func TestSetApprovalOverrideSurvivesInheritApprovalClobber(t *testing.T) {
	pool, _ := poolWithApprover(t, denyingConfig())

	var overrideCalled bool
	overrideGate := func(context.Context, string, json.RawMessage) sdkadapter.ApprovalResult {
		overrideCalled = true
		return sdkadapter.ApprovalResult{Approved: true}
	}

	conv, err := pool.CreateFreshInDir(func(*chat.Session) (string, error) { return "", nil }, "")
	if err != nil {
		t.Fatalf("CreateFreshInDir: %v", err)
	}
	sess := conv.(*Conversation).sess

	// Before the override: this session inherited the sibling's "deny"
	// policy and gate (poolWithApprover's fixture), NOT the override.
	if got := sess.ApprovalPolicyValue(); got != config.ApprovalPolicyDeny {
		t.Fatalf("pre-override policy = %q, want inherited %q", got, config.ApprovalPolicyDeny)
	}

	if err := pool.SetApprovalOverride(sess.SessionID, overrideGate, "auto"); err != nil {
		t.Fatalf("SetApprovalOverride: %v", err)
	}

	if got := sess.ApprovalPolicyValue(); !config.IsAutoPolicy(got) {
		t.Fatalf("post-override policy = %q, want auto (the override, not the inherited sibling policy)", got)
	}
	res := sess.ApprovalGate(context.Background(), "run_command", nil)
	if !overrideCalled || !res.Approved {
		t.Fatal("post-override gate is not the override gate: the sibling's inherited gate is still installed")
	}
}

// TestSetApprovalOverrideUnknownSessionErrors pins the negative path:
// an id the pool has never seen returns a named error rather than a
// silent no-op.
func TestSetApprovalOverrideUnknownSessionErrors(t *testing.T) {
	pool := NewSessionPool(nil, denyingConfig(), nil, false)
	err := pool.SetApprovalOverride("no-such-session", nil, "deny")
	if err == nil {
		t.Fatal("SetApprovalOverride(unknown id): got nil error, want rejection")
	}
}
