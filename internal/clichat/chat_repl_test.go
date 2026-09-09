package clichat

import (
	"reflect"
	"testing"
)

// TestSessionAgentContext_NilStateIsZeroValue pins the nil-state guard
// directly: a caller with no agent state attached gets the zero
// agentSessionContext, not a nil-pointer dereference on state.Context().
func TestSessionAgentContext_NilStateIsZeroValue(t *testing.T) {
	got := sessionAgentContext(nil)
	if !reflect.DeepEqual(got, agentSessionContext{}) {
		t.Fatalf("sessionAgentContext(nil) = %+v, want the zero value", got)
	}
}
