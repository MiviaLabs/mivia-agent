package chat

import (
	"errors"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// SwitchBinding's refusals.
//
// A model switch republishes the session's whole provider surface -
// completer, dispatcher, prompt budget, context binding revision. Every
// arm below is a precondition that, if it stopped holding, would publish
// a surface built against state that has since moved: a binding prepared
// for a previous agent surface, a model the workspace does not configure,
// or a budget that leaves no room to send anything.
//
// None of them fails loudly on its own. The switch would report success
// and the next turn would go out against the wrong surface.

// switchSession is a session with two configured models to switch between.
func switchSession(t *testing.T) *Session {
	t.Helper()
	return effortSession(t, &requestCaptureCompleter{}, plainModel)
}

// TestASwitchIsRefusedWhileWorkIsActive: republishing the completer under
// a running turn would swap the surface mid-request.
func TestASwitchIsRefusedWhileWorkIsActive(t *testing.T) {
	s := switchSession(t)
	binding := s.CurrentBinding()

	s.mu.Lock()
	s.activeTurns = 1
	s.mu.Unlock()

	err := s.SwitchBinding(binding)
	if err == nil {
		t.Fatal("a switch was accepted while a turn was active")
	}
	if !strings.Contains(err.Error(), "while work is active") {
		t.Errorf("error %q does not say why the switch was refused", err)
	}
}

// TestASwitchIsRefusedWhileTheSurfaceIsChanging: two switches in flight
// would race to publish, and the loser's dispatcher would leak.
func TestASwitchIsRefusedWhileTheSurfaceIsChanging(t *testing.T) {
	s := switchSession(t)
	binding := s.CurrentBinding()

	s.mu.Lock()
	s.switching = true
	s.mu.Unlock()

	err := s.SwitchBinding(binding)
	if err == nil {
		t.Fatal("a switch was accepted while the surface was changing")
	}
	if !strings.Contains(err.Error(), "session surface is changing") {
		t.Errorf("error %q does not say why the switch was refused", err)
	}
}

// TestABindingPreparedForAnOlderAgentSurfaceIsRefused: the candidate
// carries the agent-surface generation it was built against. Publishing
// one built for a surface that has since been replaced would hand the
// session a dispatcher scoped to tools the current agent no longer has.
func TestABindingPreparedForAnOlderAgentSurfaceIsRefused(t *testing.T) {
	s := switchSession(t)
	binding := s.CurrentBinding()
	binding.AgentSurfaceGeneration = 999 // prepared against a surface that never was

	err := s.SwitchBinding(binding)
	if err == nil {
		t.Fatal("a binding prepared for an outdated agent surface was published")
	}
	if !strings.Contains(err.Error(), "outdated agent surface") {
		t.Errorf("error %q does not name the stale surface", err)
	}
}

// TestAModelTheWorkspaceDoesNotConfigureIsRefused: the allowlist is what
// keeps a switch inside the operator's configured set.
func TestAModelTheWorkspaceDoesNotConfigureIsRefused(t *testing.T) {
	s := switchSession(t)
	binding := s.CurrentBinding()
	binding.Model = "some-model-nobody-configured"
	binding.Profile = config.ModelSpec{Name: "some-model-nobody-configured", ContextWindowTokens: 100000}

	err := s.SwitchBinding(binding)
	if err == nil {
		t.Fatal("an unconfigured model was published")
	}
	if !strings.Contains(err.Error(), "not configured for provider") {
		t.Errorf("error %q does not say the model is unconfigured", err)
	}
}

// TestABindingWithNoUsablePromptBudgetIsRefused: a window smaller than
// the output reserve leaves nothing to send. Publishing it would produce
// a session where every turn fails at the budget check instead of at the
// switch that caused it.
func TestABindingWithNoUsablePromptBudgetIsRefused(t *testing.T) {
	s := switchSession(t)
	// A completion reserve larger than the whole window: capacity clamps
	// to zero, which is the state that leaves nothing to send.
	reserve := 5000
	s.MaxTokens = &reserve
	binding := s.CurrentBinding()
	binding.Profile = config.ModelSpec{Name: binding.Model, ContextWindowTokens: 10}

	err := s.SwitchBinding(binding)
	if err == nil {
		t.Fatal("a binding with no usable prompt budget was published")
	}
	if !strings.Contains(err.Error(), "prompt budget") {
		t.Errorf("error %q does not name the budget", err)
	}
}

// TestAnIncompleteBindingIsRefusedBeforeThePreflight: a candidate with no
// provider or no completer cannot be published at all, and is rejected
// before the switch takes any lock.
func TestAnIncompleteBindingIsRefusedBeforeThePreflight(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func(b *ModelBinding)
		wantErr string
	}{
		{"no provider", func(b *ModelBinding) { b.ProviderName = "  " }, "incomplete model binding"},
		{"no completer", func(b *ModelBinding) { b.Completer = nil }, "incomplete model binding"},
		{"unnormalizable model", func(b *ModelBinding) { b.Model = "" }, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := switchSession(t)
			binding := s.CurrentBinding()
			tc.mutate(&binding)

			err := s.SwitchBinding(binding)
			if err == nil {
				t.Fatal("an incomplete binding was published")
			}
			if tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not say %q", err, tc.wantErr)
			}
		})
	}
}

// TestTheSwitchGuardCanRefuse: a caller-installed guard is the seam a
// surface owner uses to veto a switch it is not ready for, and its error
// must reach the caller rather than be swallowed into a generic refusal.
func TestTheSwitchGuardCanRefuse(t *testing.T) {
	s := switchSession(t)
	sentinel := errors.New("the surface owner said no")
	s.mu.Lock()
	s.switchGuard = func() error { return sentinel }
	s.mu.Unlock()

	err := s.SwitchBinding(s.CurrentBinding())
	if !errors.Is(err, sentinel) {
		t.Errorf("switch error = %v, want the guard's own error", err)
	}
}
