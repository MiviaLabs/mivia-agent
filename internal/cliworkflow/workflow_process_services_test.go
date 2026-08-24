package cliworkflow

import "testing"

func TestProcessWorkflowServicesSharesPanelLimiter(t *testing.T) {
	s1 := processWorkflowServices().panelLimiter
	s2 := processWorkflowServices().panelLimiter
	if s1 != s2 {
		t.Fatal("workflow process service did not retain the panel limiter")
	}
}
