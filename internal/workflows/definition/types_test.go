package definition

import "testing"

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
