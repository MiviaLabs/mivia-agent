package definition

import "testing"

// TestPanelFailurePolicy guards the shared panel failure-policy values used
// across admission and the controller.
func TestPanelFailurePolicy(t *testing.T) {
	if PanelFailurePolicyRequireAll != "require_all" {
		t.Fatalf("PanelFailurePolicyRequireAll = %q, want %q", PanelFailurePolicyRequireAll, "require_all")
	}
	if PanelFailurePolicyAllowPartial != "allow_partial" {
		t.Fatalf("PanelFailurePolicyAllowPartial = %q, want %q", PanelFailurePolicyAllowPartial, "allow_partial")
	}
}

// TestMaxEvidenceBindingBytes guards the shared cap that bounds one prior step
// output bound into a later step context. It must stay in sync with the
// controller's runtime cap (internal/workflows/controller/agent_step.go).
func TestMaxEvidenceBindingBytes(t *testing.T) {
	if MaxEvidenceBindingBytes != 32768 {
		t.Fatalf("MaxEvidenceBindingBytes = %d, want 32768", MaxEvidenceBindingBytes)
	}
	if MaxEvidenceBindingBytes <= 0 {
		t.Fatalf("MaxEvidenceBindingBytes = %d, want a positive bound", MaxEvidenceBindingBytes)
	}
}
