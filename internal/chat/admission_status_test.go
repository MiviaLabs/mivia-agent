package chat

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// TestPendingAdmissionStatusReportsDeferralReason: when a boundary defers, the
// session records why, and the staged-tool denial announces it mid-turn instead
// of leaving the model to probe one turn at a time (DC-9).
func TestPendingAdmissionStatusReportsDeferralReason(t *testing.T) {
	sess := newAdmissionSession(t)
	widener := &recordingWidener{}
	sess.SetSurfaceWidener(widener.fn)
	sess.SetSwitchGuard(func() error { return fmt.Errorf("background run active") })
	if _, err := sess.StageToolAdmission([]string{"grep"}, 0); err != nil {
		t.Fatalf("stage: %v", err)
	}
	sess.mu.Lock()
	sess.activeTurns = 1
	sess.mu.Unlock()
	sess.PublishPendingAdmission()
	names, reason, ok := sess.PendingAdmissionStatus()
	if !ok {
		t.Fatal("the stage must stay pending while the guard refuses")
	}
	if !slices.Equal(names, []string{"grep"}) {
		t.Fatalf("names = %v, want [grep]", names)
	}
	if !strings.Contains(reason, "background orchestration") {
		t.Fatalf("reason = %q, want the switch-guard deferral reason", reason)
	}

	sess.SetSwitchGuard(nil)
	sess.PublishPendingAdmission()
	if _, _, ok := sess.PendingAdmissionStatus(); ok {
		t.Fatal("a stage published after the guard cleared must not stay pending")
	}
}

// TestHotServeEligibleTruthTable pins the eligibility predicate behind the
// hot-serve change: a call to a PENDING-STAGED name is served synchronously on
// a stable surface instead of answered with the staged-but-not-published
// notice. The two states in which the current dispatcher itself is about to
// be replaced or closed - an agent switch in flight, and a switch guard
// refusing on behalf of background orchestration - keep the notice, because
// there the wait is real. Hot-serve never widens the surface, so the
// publication-side fencing (sole active turn, generation, supersede) does not
// apply here.
func TestHotServeEligibleTruthTable(t *testing.T) {
	sess := newAdmissionSession(t)

	if sess.hotServeEligible("grep") {
		t.Fatal("nothing is staged, so nothing can be hot-served")
	}

	if _, err := sess.StageToolAdmission([]string{"grep"}, 0); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if !sess.hotServeEligible("grep") {
		t.Fatal("a staged name on a stable surface must be hot-servable")
	}

	sess.mu.Lock()
	sess.switching = true
	sess.mu.Unlock()
	if sess.hotServeEligible("grep") {
		t.Fatal("an agent switch in flight must keep the staged notice")
	}
	sess.mu.Lock()
	sess.switching = false
	sess.mu.Unlock()

	sess.SetSwitchGuard(func() error { return fmt.Errorf("background run active") })
	if sess.hotServeEligible("grep") {
		t.Fatal("a switch guard refusing for background work must keep the staged notice")
	}
	sess.SetSwitchGuard(nil)

	if !sess.hotServeEligible("grep") {
		t.Fatal("hot-serve must resume once the guard clears")
	}

	widener := &recordingWidener{}
	sess.SetSurfaceWidener(widener.fn)
	sess.mu.Lock()
	sess.activeTurns = 1
	sess.mu.Unlock()
	sess.PublishPendingAdmission()
	if sess.hotServeEligible("grep") {
		t.Fatal("a published name has no pending stage left to hot-serve")
	}
}
