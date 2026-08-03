package chat

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// TestPlainTurnFallsBackToTheSessionContextBudget pins beginPlainTurn's budget
// fallback: a binding that carries no prompt budget of its own must inherit
// s.MaxContextTokens rather than run the turn unbounded.
//
// The assertion is indirect on purpose, because it has to distinguish "fell back"
// from "left at zero". sendPlainLegacy only enforces a budget when it is
// positive (`if snapshot.budget > 0`), so with the fallback removed the budget
// stays 0, the check is skipped entirely, and an oversized prompt is sent to the
// provider instead of refused. Asserting the refusal therefore fails if and only
// if the fallback is gone.
func TestPlainTurnFallsBackToTheSessionContextBudget(t *testing.T) {
	s := NewSession(&config.Resolved{Model: "m", SystemPrompt: "sys"}, &sessionUsageCompleter{
		usage: provider.TokenUsage{Reported: true, InputTokens: 10, OutputTokens: 5},
	})
	// Plain (non-agent) path: no tool loop, so SendUser routes through
	// beginPlainTurn.
	s.UseTools = false
	// The binding carries no budget; the session does. Only the fallback can
	// bridge the two.
	s.binding.PromptBudgetTokens = 0
	s.MaxContextTokens = 1

	_, err := s.SendUser(context.Background(), strings.Repeat("x", 400), io.Discard)
	if !errors.Is(err, agent.ErrPromptBudgetExceeded) {
		t.Fatalf("err = %v, want ErrPromptBudgetExceeded - the plain turn ignored s.MaxContextTokens", err)
	}
}

// TestPlainTurnPrefersTheBindingBudget is the other side of the same branch: a
// binding that does declare a budget must win, so the fallback cannot be written
// as an unconditional assignment.
func TestPlainTurnPrefersTheBindingBudget(t *testing.T) {
	s := NewSession(&config.Resolved{Model: "m", SystemPrompt: "sys"}, &sessionUsageCompleter{
		usage: provider.TokenUsage{Reported: true, InputTokens: 10, OutputTokens: 5},
	})
	s.UseTools = false
	// Generous binding budget, punitive session budget. If the fallback ran
	// unconditionally the session's 1 would apply and the turn would be refused.
	s.binding.PromptBudgetTokens = 100000
	s.MaxContextTokens = 1

	if _, err := s.SendUser(context.Background(), "hello", io.Discard); err != nil {
		t.Fatalf("err = %v, want the binding budget to take precedence over s.MaxContextTokens", err)
	}
}
