package chat

import (
	"context"
	"io"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

func selectSession(models ...string) *Session {
	profiles := make([]config.ModelSpec, 0, len(models))
	for _, name := range models {
		profiles = append(profiles, config.ModelSpec{Name: name, ContextWindowTokens: 100000})
	}
	return NewSession(&config.Resolved{
		ProviderName:  "zai",
		Model:         models[0],
		Models:        models,
		ModelProfiles: profiles,
	}, &requestCaptureCompleter{})
}

// SelectModel renames the selection in place, so every refusal must leave the
// session exactly as it was: a half-applied rename would leave the model name
// and the profile describing different models.
func TestSelectModelRefusalsLeaveTheSelectionAlone(t *testing.T) {
	cases := map[string]struct {
		prepare func(*Session)
		name    string
	}{
		"unnormalizable name":          {nil, "   "},
		"name with a control sequence": {nil, "bad\x1bname"},
		"not in the allowlist":         {nil, "never-configured"},
		"a turn is active": {func(s *Session) {
			s.mu.Lock()
			s.activeTurns = 1
			s.mu.Unlock()
		}, "b"},
		"the surface is switching": {func(s *Session) {
			s.mu.Lock()
			s.switching = true
			s.mu.Unlock()
		}, "b"},
	}
	for label, tc := range cases {
		t.Run(label, func(t *testing.T) {
			s := selectSession("a", "b")
			if tc.prepare != nil {
				tc.prepare(s)
			}
			if s.SelectModel(tc.name) {
				t.Fatalf("SelectModel(%q) must be refused", tc.name)
			}
			if got := s.CurrentModel(); got != "a" {
				t.Fatalf("a refused select changed the model to %q", got)
			}
		})
	}
}

// A zero generation is the compatibility case for a hand-built binding; the
// first select must produce a usable generation rather than leaving it at zero,
// which every turn snapshot treats as "no binding captured yet".
func TestSelectModelSeedsAZeroGeneration(t *testing.T) {
	s := selectSession("a", "b")
	s.mu.Lock()
	s.binding.ModelGeneration = 0
	s.mu.Unlock()
	if !s.SelectModel("b") {
		t.Fatal("SelectModel refused an allowed model")
	}
	if got := s.CurrentModelGeneration(); got != 1 {
		t.Fatalf("generation = %d, want 1", got)
	}
}

// A durable head that no longer matches means another writer moved the context
// underneath this session. SelectModel must refuse rather than rename the
// selection anyway, or the in-memory model and the durable binding revision
// describe different models.
func TestSelectModelRefusesWhenTheDurableHeadMovedOn(t *testing.T) {
	session, _, _ := clearFailureSession(t, "select-stale.db")
	session.allowedModels = []string{"model", "other"}
	if _, err := session.SendUser(context.Background(), "first question", io.Discard); err != nil {
		t.Fatal(err)
	}
	// Advance the in-memory head past the durable one so the compare-and-swap
	// inside the select is guaranteed to be rejected.
	session.mu.Lock()
	session.contextHead.Session++
	session.contextHead.Durable++
	session.mu.Unlock()

	if session.SelectModel("other") {
		t.Fatal("SelectModel must refuse when the durable head has moved on")
	}
	if got := session.CurrentModel(); got != "model" {
		t.Fatalf("a refused select changed the model to %q", got)
	}
}
