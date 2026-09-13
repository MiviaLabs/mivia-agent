package automation

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestDenyGate_DeniesAndNamesAutomation pins DenyGate's contract (D8):
// every call is refused, and the denial names the automation so a
// failed step's error message is traceable to its source.
func TestDenyGate_DeniesAndNamesAutomation(t *testing.T) {
	gate := DenyGate("nightly-summary")
	got := gate(context.Background(), "run_command", json.RawMessage(`{"command":"rm -rf /"}`))
	if got.Approved {
		t.Fatal("DenyGate approved a call; want every call denied")
	}
	if got.ApprovedForClass {
		t.Fatal("DenyGate set ApprovedForClass; a denial must never write a standing entry")
	}
	if !strings.Contains(got.Err, "nightly-summary") {
		t.Fatalf("DenyGate error = %q, want it naming the automation", got.Err)
	}
}

// TestAutoApproveGate_ApprovesWithoutStandingClass pins AutoApproveGate's
// contract (D8): every call is approved, and ApprovedForClass is always
// false so no standing-cache entry is ever written on behalf of an
// unattended run.
func TestAutoApproveGate_ApprovesWithoutStandingClass(t *testing.T) {
	gate := AutoApproveGate()
	got := gate(context.Background(), "edit_file", json.RawMessage(`{"path":"a.txt"}`))
	if !got.Approved {
		t.Fatal("AutoApproveGate denied a call; want every call approved")
	}
	if got.ApprovedForClass {
		t.Fatal("AutoApproveGate set ApprovedForClass; unattended auto-approval must not persist a standing decision")
	}
	if got.Err != "" {
		t.Fatalf("AutoApproveGate returned an error on an approved call: %q", got.Err)
	}
}
