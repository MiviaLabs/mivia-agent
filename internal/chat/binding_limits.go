package chat

// Per-session limits that ride on the active binding: the prompt budget
// (/budget) and the interactive step ceiling (/steps). They are separate from
// binding publication because they change WITHIN a generation rather than
// replacing one.

import (
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// SetPromptBudget applies the per-session prompt cap. Zero clears it and
// recomputes the selected model's configured effective capacity.
func (s *Session) SetPromptBudget(requested int) error {
	if requested < 0 {
		return fmt.Errorf("prompt budget must not be negative")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	base := promptBudget(s.binding.Profile, s.MaxTokens, s.operatorPromptCap, 0)
	if requested > base {
		return fmt.Errorf("prompt budget exceeds selected model capacity")
	}
	s.requestedPromptCap = requested
	s.binding.RequestedPromptTokens = requested
	s.binding.PromptBudgetTokens = promptBudget(s.binding.Profile, s.MaxTokens, s.operatorPromptCap, requested)
	s.MaxContextTokens = s.binding.PromptBudgetTokens
	s.invalidateLocked()
	return nil
}

// PromptBudget returns the selected model's effective prompt capacity.
func (s *Session) PromptBudget() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.binding.PromptBudgetTokens
}

// SetMaxSteps applies the per-session interactive step limit safely while a
// turn may be taking a snapshot of its options. Zero means unlimited.
func (s *Session) SetMaxSteps(steps int) error {
	if steps < 0 {
		return fmt.Errorf("max steps must not be negative")
	}
	s.mu.Lock()
	s.MaxSteps = steps
	s.invalidateLocked()
	s.mu.Unlock()
	return nil
}

// MaxStepsValue returns the current interactive step limit safely.
func (s *Session) MaxStepsValue() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.MaxSteps
}

// PromptBudgetFor computes the effective prompt capacity for a candidate
// profile while retaining this session's operator and manual caps.
func (s *Session) PromptBudgetFor(profile config.ModelSpec) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return promptBudget(profile, s.MaxTokens, s.operatorPromptCap, s.requestedPromptCap)
}

func promptBudget(profile config.ModelSpec, maxTokens *int, operatorCap, requested int) int {
	return config.EffectivePromptTokens(profile, maxTokens, operatorCap, requested)
}
