package cliorchestrate

import "testing"

func TestTaskNamespaceCanonicalizesReferenceIDs(t *testing.T) {
	if got := namespacedTaskID("call-1", " task-a "); got != "call-1:task-a" {
		t.Fatalf("namespacedTaskID = %q, want call-1:task-a", got)
	}
	deps := namespacedDependsOn("call-1", []string{" task-a "})
	if len(deps) != 1 || deps[0] != "call-1:task-a" {
		t.Fatalf("namespacedDependsOn = %v, want [call-1:task-a]", deps)
	}
}
