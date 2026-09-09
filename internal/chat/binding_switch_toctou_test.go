package chat

import (
	"strings"
	"testing"
)

// SwitchBinding re-checks under the publish lock what switchPreflight
// already checked without it. The two checks are not redundant: the
// preflight releases s.mu before running the owner's switch guard, and the
// publish path then takes contextPublishMu and s.mu. A turn that starts, or
// a surface swap that begins, inside that window would otherwise have its
// completer, dispatcher and prompt budget replaced underneath it while the
// switch reported success.
//
// The switch guard is the seam: it runs after the preflight passed and
// before the publish lock is taken, which is exactly the window under test.

// TestASwitchIsRefusedWhenATurnStartsAfterPreflight pins the post-lock
// re-check of activeTurns.
func TestASwitchIsRefusedWhenATurnStartsAfterPreflight(t *testing.T) {
	s := switchSession(t)
	binding := s.CurrentBinding()
	before := s.CurrentBinding().ModelGeneration

	s.SetSwitchGuard(func() error {
		// Background work claims the session in the window between the
		// preflight and the publish lock.
		s.mu.Lock()
		s.activeTurns = 1
		s.mu.Unlock()
		return nil
	})

	err := s.SwitchBinding(binding)
	if err == nil {
		t.Fatal("a switch published a new surface under a turn that started after the preflight")
	}
	if !strings.Contains(err.Error(), "while work is active") {
		t.Fatalf("error %q does not name the active turn", err)
	}
	if got := s.CurrentBinding().ModelGeneration; got != before {
		t.Fatalf("model generation moved to %d despite the refusal; want %d", got, before)
	}
}

// TestASwitchIsRefusedWhenTheSurfaceStartsChangingAfterPreflight pins the
// post-lock re-check of the switching flag: a session-surface swap that
// begins in the same window must win, not lose to a stale preflight.
func TestASwitchIsRefusedWhenTheSurfaceStartsChangingAfterPreflight(t *testing.T) {
	s := switchSession(t)
	binding := s.CurrentBinding()
	before := s.CurrentBinding().ModelGeneration

	s.SetSwitchGuard(func() error {
		s.mu.Lock()
		s.switching = true
		s.mu.Unlock()
		return nil
	})

	err := s.SwitchBinding(binding)
	if err == nil {
		t.Fatal("a switch published a new surface while the session surface was already changing")
	}
	if !strings.Contains(err.Error(), "session surface is changing") {
		t.Fatalf("error %q does not name the surface change", err)
	}
	if got := s.CurrentBinding().ModelGeneration; got != before {
		t.Fatalf("model generation moved to %d despite the refusal; want %d", got, before)
	}
}
