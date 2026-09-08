package uiadapter

import "testing"

func TestNamespacedTaskIDCanonicalizesReferenceIDs(t *testing.T) {
	if got := namespacedTaskID("call-1", " task-a "); got != "call-1:task-a" {
		t.Fatalf("namespacedTaskID = %q, want call-1:task-a", got)
	}
}
