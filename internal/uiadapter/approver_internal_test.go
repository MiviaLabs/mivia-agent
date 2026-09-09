package uiadapter

import (
	"context"
	"testing"
)

// TestApprover_StandingPolicyResult_NilSessionReturnsFalse pins the
// a.sess == nil guard directly: with no context-carried policy and no
// session, there is no standing policy to consult.
func TestApprover_StandingPolicyResult_NilSessionReturnsFalse(t *testing.T) {
	a := NewApprover(nil)
	if _, ok := a.standingPolicyResult(context.Background()); ok {
		t.Fatal("standingPolicyResult() ok = true, want false for a nil session with no context policy")
	}
}
