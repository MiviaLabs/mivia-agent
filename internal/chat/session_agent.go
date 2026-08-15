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
//
// advertisedToolSpecs is the binding's pinned tools[] array (plan
// tools-advertising/01): the caller computes it once from the frozen tier
// plan's admissible union, and it is what every provider request of this
// binding serializes for its whole lifetime. This is the ONLY production path
// that may set it - admission publication (TryPublishAgentSurface) must never
// touch it, or a mid-turn load_tools call would change the wire tools[] array
// and invalidate the provider's implicit prompt-cache prefix.
func (s *Session) PublishAgentSurface(prompt string, maxSteps int, registry *tools.Registry, dispatcher *runtime.Dispatcher, skillReg *skills.Registry, memoryBlock string, advertisedToolSpecs []provider.ToolSpec) {
	base := prompt
	s.mu.Lock()
	outgoing := s.prefixIdentity
	old := s.binding.Dispatcher
	s.agentSurfaceGeneration++
	s.BaseSystemPrompt = base
	s.SystemPrompt = base
	s.MaxSteps = maxSteps
	s.Tools = registry
	s.Dispatcher = dispatcher
	s.binding.Dispatcher = dispatcher
	s.binding.SkillRegistry = skillReg
	s.binding.AgentSurfaceGeneration = s.agentSurfaceGeneration
	s.advertisedToolSpecs = advertisedToolSpecs
	s.invalidateLocked()
	setSystemMessageLocked(s, base)
	setMemoryMessageLocked(s, memoryBlock)
	// The host-side agent-surface publication changes the wire prefix
	// (prompt, tools) and advances the surface generation: recapture so the
	// cache never describes the pre-publication surface (audit RC-1) and emit
	// exactly one reset naming the changed categories (INV-68-2).
	incoming := s.capturePrefixIdentityLocked()
	s.prefixIdentity = incoming
	reset := s.buildPrefixResetLocked(outgoing, incoming, true)
	s.mu.Unlock()
	if old != nil && old != dispatcher {
		old.Close()
	}
	publishPrefixResetEvent(s.EventBus, s.SessionID, reset)
}

// AdvertisedToolSpecs returns the current binding's pinned tools[] array. Set
// once per binding by PublishAgentSurface (attach / /agent / /model); never
// mutated by admission publication.
func (s *Session) AdvertisedToolSpecs() []provider.ToolSpec {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.advertisedToolSpecs
}

// SetAdvertisedToolSpecs pins the binding's tools[] array without touching any
// other surface field. It exists for the initial-attach path
// (scopeAttachedToolSurface), which scopes sess.Tools directly before the
// session dispatcher and the rest of the agent surface exist, so the full
// PublishAgentSurface publication cannot run yet. Every later change to the
// binding (/agent, /model) goes through PublishAgentSurface instead. Callers
// must call RefreshPrefixIdentity (or another identity-capture trigger)
// afterwards so the cached identity reflects the new snapshot.
func (s *Session) SetAdvertisedToolSpecs(specs []provider.ToolSpec) {
	s.mu.Lock()
	s.advertisedToolSpecs = specs
	s.mu.Unlock()
}

// SetAgentSettings updates only the root prompt and turn limit under the
// session lock, keeping the system message used by the next provider request
// consistent with the public fields.
func (s *Session) SetAgentSettings(prompt string, maxSteps int, memoryBlock string) {
	base := prompt
	s.mu.Lock()
	outgoing := s.prefixIdentity
	s.BaseSystemPrompt = base
	s.SystemPrompt = base
	s.MaxSteps = maxSteps
	s.invalidateLocked()
	setSystemMessageLocked(s, base)
	setMemoryMessageLocked(s, memoryBlock)
	// The prompt is wire-affecting: recapture and emit so a prompt change is
	// reported once and a no-op settings write stays silent (audit RC-1,
	// INV-68-2).
	incoming := s.capturePrefixIdentityLocked()
	s.prefixIdentity = incoming
	reset := s.buildPrefixResetLocked(outgoing, incoming, false)
	s.mu.Unlock()
	publishPrefixResetEvent(s.EventBus, s.SessionID, reset)
}

// RefreshPrefixIdentity recaptures the cached prefix identity after a
// host-side tool-surface mutation that is not one of the trigger events
// (attach-time sess.Tools wiring in the CLI). It emits a KindPrefixReset when
// the wire-affecting subset changed, so the cache never describes a stale
// surface and the next trigger cannot emit a false reset (audit RC-1,
// INV-68-2).
func (s *Session) RefreshPrefixIdentity() {
	s.mu.Lock()
	outgoing := s.prefixIdentity
	incoming := s.capturePrefixIdentityLocked()
	s.prefixIdentity = incoming
	reset := s.buildPrefixResetLocked(outgoing, incoming, true)
	s.mu.Unlock()
	publishPrefixResetEvent(s.EventBus, s.SessionID, reset)
}

// AgentSettings returns the current root prompt and turn limit atomically.
// The returned prompt is BaseSystemPrompt (memory-block-free), not
// SystemPrompt (plan 77, E3) - callers read-modify-write this value
// (appending a deferred-tool index, capturing a switch baseline) and pass
// it back through SetAgentSettings/PublishAgentSurface, which recompose
// the memory block fresh; returning the composed value here would let it
// duplicate on every such cycle.
func (s *Session) AgentSettings() (string, int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.BaseSystemPrompt, s.MaxSteps
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

// setMemoryMessageLocked maintains the session-owned core-memory context
// message: a user-role message carrying the framed block, kept at the one
// position immediately after the system message (index 1, or 0 when there is
// no system message) so it always precedes the first real user objective.
//
// It lives in the conversation rather than the system prompt so a memory
// promotion no longer changes the system message - the first explicitly
// cache-marked block (markStablePrefixCacheControl) - and so no longer
// invalidates tools + system + the whole history cache. A mid-session memory
// refresh still rewrites this message and therefore invalidates the provider
// cache from index 1 onward; that is deliberate and still far cheaper than
// invalidating the stable prefix itself. When this frame is the FIRST user
// message, the provider's first-user-message cache marker lands on it, which
// is fine: it is stable within a session between memory changes.
//
// s.memoryContext mirrors the current frame so /clear (resetSystem) can
// re-seed it.
func setMemoryMessageLocked(s *Session, memoryBlock string) {
	content := MemoryContextContent(memoryBlock)
	s.memoryContext = content
	idx := 0
	if len(s.Messages) > 0 && s.Messages[0].Role == provider.RoleSystem {
		idx = 1
	}
	has := len(s.Messages) > idx && isMemoryContextMessage(s.Messages[idx])
	switch {
	case content == "" && has:
		s.Messages = append(s.Messages[:idx], s.Messages[idx+1:]...)
	case content == "":
		// No frame wanted, none present: a true no-op.
	case has:
		s.Messages[idx].Content = content
	default:
		s.Messages = append(s.Messages[:idx], append([]provider.Message{{Role: provider.RoleUser, Content: content, Name: MemoryContextMessageName}}, s.Messages[idx:]...)...)
	}
}

// reseedMemoryFrameLocked re-places the session-owned core-memory frame at its
// canonical position when a history adoption dropped it. It is the
// belt-and-braces half of compaction preservation: the planner keeps the frame
// on purpose (PlanInput.PreserveNames), and this guard re-inserts it from the
// session's own mirror if a retention change ever forgets the seam. The mirror
// already holds the rendered block, so re-seeding never re-renders: it mirrors
// setMemoryMessageLocked except that it takes s.memoryContext verbatim, the
// same value /clear re-seeds from.
func reseedMemoryFrameLocked(s *Session) {
	content := s.memoryContext
	if content == "" {
		return
	}
	idx := 0
	if len(s.Messages) > 0 && s.Messages[0].Role == provider.RoleSystem {
		idx = 1
	}
	if len(s.Messages) > idx && isMemoryContextMessage(s.Messages[idx]) {
		return
	}
	s.Messages = append(s.Messages[:idx], append([]provider.Message{{Role: provider.RoleUser, Content: content, Name: MemoryContextMessageName}}, s.Messages[idx:]...)...)
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
