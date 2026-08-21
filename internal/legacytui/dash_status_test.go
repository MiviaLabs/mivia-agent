package legacytui

import "testing"

// TestDashStatusLocalConstWireBytes pins dashboard-only compounds stay byte-identical.
func TestDashStatusLocalConstWireBytes(t *testing.T) {
	if taskStatusRetryQueued != "retry_queued" {
		t.Fatalf("taskStatusRetryQueued = %q", taskStatusRetryQueued)
	}
	if taskStatusInterruptedUnrecoverable != "interrupted_unrecoverable" {
		t.Fatalf("taskStatusInterruptedUnrecoverable = %q", taskStatusInterruptedUnrecoverable)
	}
	if dashStatusDegraded != "degraded" {
		t.Fatalf("dashStatusDegraded = %q", dashStatusDegraded)
	}
	if dashStatusUnknown != "unknown" {
		t.Fatalf("dashStatusUnknown = %q", dashStatusUnknown)
	}
}
