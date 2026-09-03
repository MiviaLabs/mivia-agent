package chat

import (
	"errors"
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

// SwitchBinding atomically publishes a fully prepared idle binding.
func (s *Session) SwitchBinding(binding ModelBinding) error {
	binding, current, err := s.prepareSwitchBinding(binding)
	if err != nil {
		return err
	}
	s.contextPublishMu.Lock()
	defer s.contextPublishMu.Unlock()
	s.mu.Lock()
	if s.activeTurns > 0 {
		return s.refuseSwitchLocked(binding, s.binding.Dispatcher, errors.New("model switching is unavailable while work is active"))
	}
	if s.switching {
		return s.refuseSwitchLocked(binding, s.binding.Dispatcher, errors.New("model switching is unavailable while session surface is changing"))
	}
	if binding.AgentSurfaceGeneration != 0 && binding.AgentSurfaceGeneration != s.agentSurfaceGeneration {
		return s.refuseSwitchLocked(binding, s.binding.Dispatcher, errors.New("model binding was prepared for an outdated agent surface"))
	}
	if !s.bindingAllowsLocked(binding.ProviderName, binding.Model) {
		return s.refuseSwitchLocked(binding, s.binding.Dispatcher, fmt.Errorf("model is not configured for provider %s", binding.ProviderName))
	}
	binding.RequestedPromptTokens = s.requestedPromptCap
	binding.PromptBudgetTokens = promptBudget(binding.Profile, s.MaxTokens, s.operatorPromptCap, s.requestedPromptCap)
	if binding.PromptBudgetTokens <= 0 {
		return s.refuseSwitchLocked(binding, s.binding.Dispatcher, errors.New("model has no usable prompt budget"))
	}
	binding.ModelGeneration = s.binding.ModelGeneration + 1
	contextStore := s.contextStore
	contextPrincipal := s.contextPrincipal
	contextWorktree := s.contextWorktree
	contextExpected := s.contextHead
	contextEnabled := s.contextEnabledLocked() && contextStore != nil
	expectedBinding := captureBindingRevision(s.binding)
	newBinding := captureBindingRevision(binding)
	if err := s.advanceBindingIfNeeded(contextEnabled, contextStore, contextPrincipal, contextWorktree, contextExpected, expectedBinding, newBinding, "switch"); err != nil {
		return s.refuseSwitchLocked(binding, current, fmt.Errorf("advance context binding: %w", err))
	}
	// Reasoning replay is provider-scoped: chain-of-thought belongs to the
	// provider that produced it, so a generation that moves to a different
	// provider drops the bytes rather than ship another provider's reasoning
	// to a backend that never produced it (see stripReasoningForProviderSwitch).
	s.stripReasoningForProviderSwitch(binding)
	outgoing := s.prefixIdentity
	old := s.publishBindingLocked(binding)
	s.invalidateLocked()
	if contextEnabled {
		s.contextHead = contextstate.Revision{Session: contextExpected.Session + 1, Durable: contextExpected.Durable + 1, Source: contextExpected.Source}
	}
	// SwitchBinding is one of the four identity-capture triggers (INV-68-8).
	// The incoming identity reflects the newly published binding; when the
	// wire-affecting subset changed, exactly one KindPrefixReset event is
	// built under the lock and published after unlock (INV-68-2).
	incoming := s.capturePrefixIdentityLocked()
	s.prefixIdentity = incoming
	reset := s.buildPrefixResetLocked(outgoing, incoming, false)
	warn := s.checkWarnUnknownModelLocked(binding.Model, binding.FallbackProfile)
	bus := s.EventBus
	s.mu.Unlock()
	if warn && WarnUnknownContextWindow != nil {
		WarnUnknownContextWindow(binding.Model)
	}
	publishPrefixResetEvent(bus, s.SessionID, reset)
	if old.Dispatcher != nil && old.Dispatcher != binding.Dispatcher {
		old.Dispatcher.Close()
	}
	return nil
}

// prepareSwitchBinding validates and normalizes an incoming binding before the
// switch critical section, closing the candidate dispatcher on any refusal. It
// returns the normalized binding and the active dispatcher snapshot captured by
// preflight; the caller must not reuse the candidate after an error.
func (s *Session) prepareSwitchBinding(binding ModelBinding) (ModelBinding, *runtime.Dispatcher, error) {
	name, err := config.NormalizeModelName(binding.Model)
	if err != nil {
		closeUnpublishedDispatcher(binding.Dispatcher, nil)
		return binding, nil, err
	}
	if strings.TrimSpace(binding.ProviderName) == "" || binding.Completer == nil {
		closeUnpublishedDispatcher(binding.Dispatcher, nil)
		return binding, nil, fmt.Errorf("incomplete model binding")
	}
	binding.Model = name
	current, err := s.switchPreflight()
	if err != nil {
		closeUnpublishedDispatcher(binding.Dispatcher, current)
		return binding, current, err
	}
	return binding, current, nil
}

// stripReasoningForProviderSwitch drops reasoning_content from the in-memory
// history when a new binding moves the session to a DIFFERENT provider:
// chain-of-thought belongs to the provider that produced it, and replaying one
// provider's CoT to another backend is cross-model contamination. A
// same-provider model change keeps the bytes — both DeepSeek models are
// replay-capable, and stripping there would break the D2 gate. Runs inside the
// SwitchBinding critical section (s.mu held); a concurrent turn cannot start
// mid-strip.
func (s *Session) stripReasoningForProviderSwitch(binding ModelBinding) {
	if binding.ProviderName != s.binding.ProviderName {
		for i := range s.Messages {
			s.Messages[i].ReasoningContent = ""
		}
	}
}

func (s *Session) switchPreflight() (*runtime.Dispatcher, error) {
	s.mu.Lock()
	if s.activeTurns > 0 {
		current := s.binding.Dispatcher
		s.mu.Unlock()
		return current, fmt.Errorf("model switching is unavailable while work is active")
	}
	if s.switching {
		current := s.binding.Dispatcher
		s.mu.Unlock()
		return current, fmt.Errorf("model switching is unavailable while session surface is changing")
	}
	current, guard := s.binding.Dispatcher, s.switchGuard
	s.mu.Unlock()
	if guard != nil {
		if err := guard(); err != nil {
			return current, err
		}
	}
	return current, nil
}

func closeUnpublishedDispatcher(candidate, current *runtime.Dispatcher) {
	if candidate != nil && candidate != current {
		candidate.Close()
	}
}

// refuseSwitchLocked refuses a switch whose preconditions fail under the
// session lock: it closes the unpublished candidate (unless it is the active
// dispatcher) and releases the lock. Callers hold s.mu and must return the
// error immediately.
func (s *Session) refuseSwitchLocked(binding ModelBinding, current *runtime.Dispatcher, err error) error {
	s.mu.Unlock()
	closeUnpublishedDispatcher(binding.Dispatcher, current)
	return err
}

// SetBindingFactory wires CLI-owned provider construction for exact session
// restore. The factory must prepare a complete binding without mutating s.
func (s *Session) SetBindingFactory(factory func(providerName, model string) (ModelBinding, error)) {
	s.mu.Lock()
	s.bindingFactory = factory
	s.mu.Unlock()
}

// SetSwitchGuard installs an owner callback for work that outlives the active
// chat turn. The callback can prevent replacing this session's generation
// while background orchestration still owns it.
func (s *Session) SetSwitchGuard(guard func() error) {
	s.mu.Lock()
	s.switchGuard = guard
	s.mu.Unlock()
}

// CheckSwitchAllowed reports whether owner-managed background work permits a
// session replacement or model switch.
func (s *Session) CheckSwitchAllowed() error {
	s.mu.RLock()
	guard := s.switchGuard
	s.mu.RUnlock()
	if guard != nil {
		return guard()
	}
	return nil
}
