package chat

import (
	"fmt"
	"slices"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
)

// ReasoningChoices is the ordered set of efforts the active model offers, in
// the order its configuration lists them. An empty result means this model has
// no reasoning surface, which is what /effort reports instead of an empty
// picker.
func (s *Session) ReasoningChoices() []reasoning.Level {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.binding.Profile.ReasoningEfforts)
}

// ReasoningEffort is the level the next request will carry: the user's /effort
// choice when they made one, otherwise the model's configured default.
func (s *Session) ReasoningEffort() reasoning.Level {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.effectiveReasoningLocked()
}

// ReasoningSetting is the whole dial the next request will carry: the
// effective level paired with the dialect that will express it. Callers outside
// the session that must send what the session sends take the pair from here, so
// a level and a dialect resolved at different moments cannot drift apart.
//
// The dialect is resolved against the bound provider, not returned as the model
// wrote it. A model entry that leaves reasoning_dialect out still reaches the
// wire in its provider's vetted shape, and a caller handed the empty string
// would have to repeat that lookup or describe a request that is not the one
// being sent.
func (s *Session) ReasoningSetting() reasoning.Setting {
	s.mu.RLock()
	defer s.mu.RUnlock()
	profile := s.binding.Profile
	profile.Reasoning = s.effectiveReasoningLocked()
	return reasoning.Resolve(s.binding.ProviderName, config.ModelReasoning(profile))
}

// ReasoningDefault is the active model's configured default, independent of
// any /effort choice. The picker labels it so the user can tell what they are
// departing from.
func (s *Session) ReasoningDefault() reasoning.Level {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.binding.Profile.Reasoning
}

// ReasoningOverride reports the user's /effort choice for the current binding
// and whether one is recorded at all. A choice that names the model's own
// configured default is indistinguishable from an untouched dial by level
// alone, so a caller asking "did the user choose this" cannot answer it by
// subtracting ReasoningEffort from ReasoningDefault.
//
// The pair is returned together because both halves are read under one lock:
// asking for the flag and the level separately reintroduces the two-reading
// drift, and the level alone is only safe for a caller that knows a stored
// override is always active.
func (s *Session) ReasoningOverride() (reasoning.Level, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.reasoningEffort, s.reasoningEffort.Active()
}

// SetReasoningEffort applies a /effort choice for the active model, or clears
// the choice back to the model's configured default when the level is unset.
//
// An active level must be one the model declared: the declared set is the
// contract, and sending a level outside it would earn a provider 400 the user
// cannot diagnose from the picker they were shown. A refusal leaves the
// previous effort in force rather than clearing it.
//
// The clear is spelled as the empty level rather than as its own method
// because the empty level already means "unset" everywhere in
// internal/reasoning, and a model that declares efforts with no configured
// default ships in exactly that state. A second verb would be a second
// vocabulary for a value the dial already holds.
func (s *Session) SetReasoningEffort(level reasoning.Level) error {
	// The dial belongs to the binding, and background orchestration outlives
	// the turn that spawned it while its nested handlers read the dial live on
	// every step. So whatever forbids replacing the binding forbids re-grading
	// the work already running against it. The guard is a caller-owned callback
	// and must not be invoked under s.mu.
	if err := s.CheckSwitchAllowed(); err != nil {
		return err
	}
	s.mu.Lock()
	// Same rule as /model: an in-flight turn already captured its binding, so
	// changing the dial now would report a change the running request did not
	// get.
	if s.activeTurns > 0 {
		s.mu.Unlock()
		return fmt.Errorf("reasoning effort cannot change while work is active")
	}
	var reset *events.PrefixResetEvent
	if !level.Active() {
		s.reasoningEffort = ""
		s.invalidateLocked()
		s.bumpPrefixGenerationLocked()
		// /effort is wire-affecting (reasoningFields) and one of the four
		// capture triggers (INV-68-8): emit exactly one reset naming
		// "reasoning" so the implicit-cache-prefix break is observable (audit
		// RC-3, INV-68-2). A same-level no-op emits nothing (the generation
		// counters never produce a category).
		reset = s.emitReasoningPrefixResetLocked()
		s.mu.Unlock()
		publishPrefixResetEvent(s.EventBus, s.SessionID, reset)
		return nil
	}
	profile := s.binding.Profile
	if !config.ModelOffersReasoning(profile) {
		s.mu.Unlock()
		return fmt.Errorf("model %q declares no reasoning efforts", s.binding.Model)
	}
	if !slices.Contains(profile.ReasoningEfforts, level) {
		s.mu.Unlock()
		return fmt.Errorf("model %q does not offer reasoning effort %q (offers %s)",
			s.binding.Model, level, reasoning.FormatLevels(profile.ReasoningEfforts))
	}
	s.reasoningEffort = level
	s.invalidateLocked()
	// /effort changes the request body via reasoningFields in a way
	// BindingFence cannot see (gap B13): the generation bump plus the
	// recapture make identities before/after /effort provably unequal, and the
	// refusal paths above leave the identity cache untouched (no false reset).
	s.bumpPrefixGenerationLocked()
	reset = s.emitReasoningPrefixResetLocked()
	s.mu.Unlock()
	publishPrefixResetEvent(s.EventBus, s.SessionID, reset)
	return nil
}

// emitReasoningPrefixResetLocked recaptures the identity after a /effort
// change, returns the KindPrefixReset event (nil when the wire-affecting
// subset is unchanged), and leaves the cache at the fresh capture. Callers
// hold s.mu and publish the returned event after unlock.
func (s *Session) emitReasoningPrefixResetLocked() *events.PrefixResetEvent {
	outgoing := s.prefixIdentity
	incoming := s.capturePrefixIdentityLocked()
	s.prefixIdentity = incoming
	return s.buildPrefixResetLocked(outgoing, incoming, false)
}

// effectiveReasoningLocked resolves the override against the model default.
func (s *Session) effectiveReasoningLocked() reasoning.Level {
	if s.reasoningEffort.Active() {
		return s.reasoningEffort
	}
	return s.binding.Profile.Reasoning
}
