package config

import "testing"

// Pins the values in one place, so an accidental edit to a shared
// timing/limit/threshold shows up as a diff here instead of silently
// changing view-layer behaviour.
func TestDefaults(t *testing.T) {
	if TextDeltaFlushInterval <= 0 {
		t.Errorf("TextDeltaFlushInterval must be positive, got %v", TextDeltaFlushInterval)
	}
	if SpinnerFPS <= 0 {
		t.Errorf("SpinnerFPS must be positive, got %d", SpinnerFPS)
	}
	if SessionPickerRefreshInterval <= 0 {
		t.Errorf("SessionPickerRefreshInterval must be positive, got %v", SessionPickerRefreshInterval)
	}
	if MaxTranscriptLines <= 0 {
		t.Errorf("MaxTranscriptLines must be positive, got %d", MaxTranscriptLines)
	}
	if MaxToolOutputBytes <= 0 {
		t.Errorf("MaxToolOutputBytes must be positive, got %d", MaxToolOutputBytes)
	}
	if !(WCAGAABody > WCAGAALarge && WCAGAALarge > 0) {
		t.Errorf("expected WCAGAABody (%v) > WCAGAALarge (%v) > 0", WCAGAABody, WCAGAALarge)
	}
	if WCAGAAALarge <= WCAGAABody {
		t.Errorf("expected WCAGAAALarge (%v) > WCAGAABody (%v)", WCAGAAALarge, WCAGAABody)
	}
	if CVDSeparationThreshold <= 0 {
		t.Errorf("CVDSeparationThreshold must be positive, got %v", CVDSeparationThreshold)
	}
	if ApprovalDefaultInline != "once" {
		t.Errorf("ApprovalDefaultInline = %q, want %q", ApprovalDefaultInline, "once")
	}
	if ApprovalDefaultDialog != "deny" {
		t.Errorf("ApprovalDefaultDialog = %q, want %q", ApprovalDefaultDialog, "deny")
	}
}
