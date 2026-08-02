package chat

import (
	"fmt"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// BeginSurfaceSwitch reserves the session while a caller builds and publishes
// a complete agent/model surface. New turns and competing switches fail closed
// until the returned release function is called.
func (s *Session) BeginSurfaceSwitch() (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeTurns > 0 {
		return nil, fmt.Errorf("session switching is unavailable while work is active")
	}
	if s.switching {
		return nil, fmt.Errorf("session switching is already in progress")
	}
	if s.loading {
		return nil, fmt.Errorf("session switching is unavailable while a session is loading")
	}
	s.switching = true
	return func() {
		s.mu.Lock()
		s.switching = false
		s.mu.Unlock()
	}, nil
}

// BeginSessionLoad reserves the session for Session.Load, which mutates every
// surface a switch does: it replaces history, advances turnID, and rebuilds the
// tool surface from the persisted admitted set. Without a reservation it was
// the only such entry point that could run beside a live turn, and its
// admission replay then wrote a decision made from a stale snapshot over the
// turn's own publication (plan tools/05).
//
// It is deliberately NOT BeginSurfaceSwitch: switching also fails closed
// against surface publication, and a load publishes one itself through the
// host's widener. loading blocks new turns and competing switches only.
func (s *Session) BeginSessionLoad() (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeTurns > 0 {
		return nil, fmt.Errorf("loading a session is unavailable while work is active")
	}
	if s.switching {
		return nil, fmt.Errorf("loading a session is unavailable while the session surface is changing")
	}
	if s.loading {
		return nil, fmt.Errorf("a session load is already in progress")
	}
	s.loading = true
	return func() {
		s.mu.Lock()
		s.loading = false
		s.mu.Unlock()
	}, nil
}

// SetEventIdentityFactory installs the CLI-owned typed identity source used by
// lifecycle events. The factory is sampled once per turn generation.
func (s *Session) SetEventIdentityFactory(factory func(uint64) *events.Identity) {
	s.mu.Lock()
	s.eventIdentity = factory
	s.mu.Unlock()
}

// PublishAgentSurface atomically publishes root-agent prompt, turn settings,
// scoped tools, dispatcher, and skill registry after candidate construction.
func (s *Session) PublishAgentSurface(prompt string, maxSteps int, registry *tools.Registry, dispatcher *runtime.Dispatcher, skillReg *skills.Registry) {
	s.mu.Lock()
	old := s.binding.Dispatcher
	s.agentSurfaceGeneration++
	s.SystemPrompt = prompt
	s.MaxSteps = maxSteps
	s.Tools = registry
	s.Dispatcher = dispatcher
	s.binding.Dispatcher = dispatcher
	s.binding.SkillRegistry = skillReg
	s.binding.AgentSurfaceGeneration = s.agentSurfaceGeneration
	s.invalidateLocked()
	setSystemMessageLocked(s, prompt)
	s.mu.Unlock()
	if old != nil && old != dispatcher {
		old.Close()
	}
}

// SetAgentSettings updates only the root prompt and turn limit under the
// session lock, keeping the system message used by the next provider request
// consistent with the public fields.
func (s *Session) SetAgentSettings(prompt string, maxSteps int) {
	s.mu.Lock()
	s.SystemPrompt = prompt
	s.MaxSteps = maxSteps
	s.invalidateLocked()
	setSystemMessageLocked(s, prompt)
	s.mu.Unlock()
}

// AgentSettings returns the current root prompt and turn limit atomically.
func (s *Session) AgentSettings() (string, int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.SystemPrompt, s.MaxSteps
}

// AgentSurfaceSnapshot returns the mutable session surface under one lock.
// The registry itself is immutable after publication; callers may safely
// derive a candidate from the returned pointer after the lock is released.
func (s *Session) AgentSurfaceSnapshot() (*tools.Registry, int, uint64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Tools, s.MaxToolResultChars, s.agentSurfaceGeneration
}

// AgentTurnEnabled reads the turn mode and tool surface as one safe snapshot.
func (s *Session) AgentTurnEnabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.UseTools && s.Tools != nil
}

func setSystemMessageLocked(s *Session, prompt string) {
	if len(s.Messages) == 0 {
		if prompt != "" {
			s.Messages = []provider.Message{{Role: provider.RoleSystem, Content: prompt}}
		}
	} else if s.Messages[0].Role == provider.RoleSystem {
		if prompt == "" {
			s.Messages = s.Messages[1:]
		} else {
			s.Messages[0].Content = prompt
		}
	} else if prompt != "" {
		s.Messages = append([]provider.Message{{Role: provider.RoleSystem, Content: prompt}}, s.Messages...)
	}
}

// SetDispatcher attaches the startup dispatcher to the current binding
// generation. This keeps the initial generation subject to the same lifecycle
// boundary as every later model switch.
func (s *Session) SetDispatcher(dispatcher *runtime.Dispatcher) {
	s.mu.Lock()
	old := s.binding.Dispatcher
	s.binding.Dispatcher = dispatcher
	s.Dispatcher = dispatcher
	s.mu.Unlock()
	if old != nil && old != dispatcher {
		old.Close()
	}
}

// CloseDispatcher closes the dispatcher that is live at call time.
//
// Session cleanup must not capture the dispatcher it saw at attach: every
// /agent switch, model switch and tool admission publishes a new one and closes
// the old, so a captured pointer names a corpse and the live dispatcher's
// OnClose hooks (coordinator and ledger teardown) would never run. Close is
// idempotent, so this is safe when the two coincide.
func (s *Session) CloseDispatcher() {
	if s == nil {
		return
	}
	s.mu.Lock()
	dispatcher := s.binding.Dispatcher
	if dispatcher == nil {
		dispatcher = s.Dispatcher
	}
	s.mu.Unlock()
	if dispatcher != nil {
		dispatcher.Close()
	}
}

// SetBindingSkillRegistry attaches the startup skill registry to the current
// immutable generation. Later model switches publish their registry through
// ModelBinding, so callers never observe a dispatcher/catalog mismatch.
func (s *Session) SetBindingSkillRegistry(registry *skills.Registry) {
	s.mu.Lock()
	s.binding.SkillRegistry = registry
	s.mu.Unlock()
}
