package chat

import (
	"context"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// TestBlankUserMessageIsRefused is the regression for a session that could be
// poisoned by one keystroke. The wire shape gate rejects a user message whose
// content trims to nothing, and it runs on every later preparation, so a blank
// turn did not fail by itself: it failed the NEXT turn and every turn after it
// with "empty user message at index N", pointing at a message nobody meant to
// send. Whitespace is the reachable case, because a composer holding a stray
// space or newline is not the empty string.
func TestBlankUserMessageIsRefused(t *testing.T) {
	for _, text := range []string{"", " ", "\n", "\t\n  ", "  \r\n "} {
		s := NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, &fakeCompleter{out: "answer"})
		s.Messages = []provider.Message{{Role: provider.RoleSystem, Content: "system"}}
		before := len(s.Messages)

		_, err := s.SendUser(context.Background(), text, nil)
		if err == nil {
			t.Errorf("SendUser(%q) was accepted; a blank turn makes every later turn unpreparable", text)
			continue
		}
		if !strings.Contains(err.Error(), "blank") {
			t.Errorf("SendUser(%q) error = %q, want it to name the blank message", text, err)
		}
		if len(s.Messages) != before {
			t.Errorf("SendUser(%q) still touched the history: %d messages, want %d", text, len(s.Messages), before)
		}
		if err := validateSessionShape(s.Messages); err != nil {
			t.Errorf("history is no longer preparable after a refused send: %v", err)
		}
	}
}

// validateSessionShape runs the same gate every preparation runs, so the test
// fails for the reason the session would actually fail later.
func validateSessionShape(messages []provider.Message) error {
	masked := make([]provider.Message, len(messages))
	copy(masked, messages)
	for i := range masked {
		if masked[i].Role == provider.RoleUser {
			masked[i].Name = ""
		}
	}
	return provider.ValidateToolPairing(masked)
}
