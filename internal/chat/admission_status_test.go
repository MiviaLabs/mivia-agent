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
