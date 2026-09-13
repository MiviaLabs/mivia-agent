package ports

import "testing"

// TestResumeAutomationRunSatisfiesAutomationEdit pins
// ResumeAutomationRun.isAutomationEdit() (settings_automations.go):
// unlike its four siblings (UpsertAutomation, RemoveAutomation,
// SetAutomationEnabled, TriggerAutomation - each already constructed
// and applied through internal/uiadapter and internal/automation's own
// tests), nothing anywhere calls ResumeAutomationRun{} directly yet
// (resume execution is chunk 8's job; today only Apply's default-case
// "not yet implemented" error path names the type at all). Assigning it
// to the AutomationEdit interface variable is what actually invokes the
// marker method, proving it satisfies the closed union it was added to.
func TestResumeAutomationRunSatisfiesAutomationEdit(t *testing.T) {
	edit := ResumeAutomationRun{RunID: "run-1"}
	edit.isAutomationEdit() // the marker method itself: called directly so its (empty) body registers as executed, not just assigned through the interface.
	var e AutomationEdit = edit
	if _, ok := e.(ResumeAutomationRun); !ok {
		t.Fatalf("AutomationEdit holding ResumeAutomationRun type-asserted back as %T", e)
	}
}

// TestCancelAutomationRunSatisfiesAutomationEdit mirrors
// TestResumeAutomationRunSatisfiesAutomationEdit above for
// CancelAutomationRun (settings_automations.go): proves the marker
// method is implemented and the type round-trips through the closed
// union.
func TestCancelAutomationRunSatisfiesAutomationEdit(t *testing.T) {
	edit := CancelAutomationRun{RunID: "run-1"}
	edit.isAutomationEdit()
	var e AutomationEdit = edit
	if _, ok := e.(CancelAutomationRun); !ok {
		t.Fatalf("AutomationEdit holding CancelAutomationRun type-asserted back as %T", e)
	}
}
