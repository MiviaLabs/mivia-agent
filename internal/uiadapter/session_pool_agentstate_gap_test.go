package uiadapter

import (
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"testing"
)

// TestBindEntryStateLockedInitializesNilMap covers bindEntryStateLocked's
// lazy agentStates init directly: NewSessionPool always pre-creates the map,
// so no test through the constructor ever sees it nil.
func TestBindEntryStateLockedInitializesNilMap(t *testing.T) {
	p := &SessionPool{}
	p.bindEntryStateLocked("id-1", &cliagents.AgentSessionState{})
	if p.agentStates == nil {
		t.Fatal("bindEntryStateLocked did not initialize a nil agentStates map")
	}
	if p.agentStates["id-1"] == nil {
		t.Fatal("bindEntryStateLocked did not register the state under id")
	}
}

// TestApplyApprovalDefaultSkipsNilAndDuplicateSessions covers the dedup
// loop's two skip branches directly, driven through a minimal SessionPool
// construction rather than the full pool lifecycle.
func TestApplyApprovalDefaultSkipsNilAndDuplicateSessions(t *testing.T) {
	res := &config.Resolved{}
	sess := chat.NewSession(res, nil)

	p := &SessionPool{
		sessions: map[string]*chat.Session{
			"nil-entry": nil,
			"first":     sess,
			"second":    sess, // same pointer as "first": must be deduped
		},
	}

	// Must not panic on the nil entry, and must apply the policy exactly
	// once despite the session appearing twice in the map.
	p.ApplyApprovalDefault("deny")

	if got := sess.ApprovalPolicyValue(); got != config.ApprovalPolicyDeny {
		t.Fatalf("session approval policy = %q, want %q", got, config.ApprovalPolicyDeny)
	}
}
