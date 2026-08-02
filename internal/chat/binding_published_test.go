package chat

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

func publishedBindingSession() *Session {
	return NewSession(&config.Resolved{
		ProviderName: "zai",
		Model:        "glm-5.2",
		Models:       []string{"glm-5.2"},
		ModelProfiles: []config.ModelSpec{{
			Name: "glm-5.2", ContextWindowTokens: 200000,
			ReasoningEfforts: []reasoning.Level{reasoning.Low, reasoning.High},
			Reasoning:        reasoning.High,
			ReasoningDialect: reasoning.DialectThinkingEffort,
		}},
	}, &requestCaptureCompleter{})
}

// The two accessors answer different questions, and a caller that round-trips a
// binding back into the session needs the configured one.
func TestPublishedBindingExcludesTheEffortOverride(t *testing.T) {
	s := publishedBindingSession()
	if err := s.SetReasoningEffort(reasoning.Low); err != nil {
		t.Fatal(err)
	}
	if got := s.CurrentBinding().Profile.Reasoning; got != reasoning.Low {
		t.Fatalf("captured binding carried %q, want the override low", got)
	}
	if got := s.PublishedBinding().Profile.Reasoning; got != reasoning.High {
		t.Fatalf("published binding carried %q, want the configured default high", got)
	}
	if got := s.ReasoningEffort(); got != reasoning.Low {
		t.Fatalf("reading the published binding disturbed the override: %q", got)
	}
}

// The published binding is still a usable turn input: the accessor reconciles
// the same legacy fields the capture path does.
func TestPublishedBindingReconcilesLegacySessionFields(t *testing.T) {
	s := publishedBindingSession()
	s.binding.Completer = nil
	s.model = "glm-5.2"
	binding := s.PublishedBinding()
	if binding.Completer == nil {
		t.Fatal("published binding lost the session completer")
	}
	if binding.Model != "glm-5.2" {
		t.Fatalf("published binding model = %q", binding.Model)
	}
}

// The published binding reconciles the legacy mirror fields the same way a
// captured one does, so a caller that starts from it is not handed a binding
// missing the dispatcher the session actually holds.
func TestPublishedBindingAdoptsTheSessionDispatcher(t *testing.T) {
	s := NewSession(&config.Resolved{
		ProviderName:  "zai",
		Model:         "m",
		ModelProfiles: []config.ModelSpec{{Name: "m", ContextWindowTokens: 100000}},
	}, &requestCaptureCompleter{})
	s.mu.Lock()
	s.binding.Dispatcher = nil
	s.binding.Completer = nil
	s.mu.Unlock()

	dispatcher, err := runtime.NewToolDispatcher(tools.NewRegistry(), runtime.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	s.Dispatcher = dispatcher

	published := s.PublishedBinding()
	if published.Dispatcher != dispatcher {
		t.Fatal("published binding did not adopt the session dispatcher")
	}
	if published.Completer == nil {
		t.Fatal("published binding did not adopt the session completer")
	}
	// The adoption lands on the copy; the session's own binding is untouched
	// because PublishedBinding holds only the read lock.
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.binding.Dispatcher != nil {
		t.Fatal("PublishedBinding wrote back into the session binding")
	}
}
