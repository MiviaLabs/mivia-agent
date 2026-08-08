package cli

import "testing"

func TestProcessWorkflowServicesSharesPanelLimiter(t *testing.T) {
	if processWorkflowServices().panelLimiter != processWorkflowServices().panelLimiter {
		t.Fatal("workflow process service did not retain the panel limiter")
	}
}
