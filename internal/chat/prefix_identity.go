package chat

import (
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
)

// PrefixIdentity is the session's byte-prefix stability identity (plan 68).
// The wire-affecting fields are exactly the inputs to the stable request
// prefix that the trigger events can change: provider/model (temperature rides
// with the model), the effective reasoning dial (level and provider-resolved
// dialect), the tool-schema digest, the system-prompt digest, and the
// memory-context digest (the core-memory frame rides as the user-role message
// at index 1, so a memory promotion changes wire bytes without touching the
// system prompt). Equality of those fields is necessary and sufficient for
// byte-equal request prefixes (INV-68-1). The two generation counters ride along as observability only:
// they never gate equality by themselves, because a republish that only
// advances a counter is byte-stable and must not emit a false reset (INV-68-2,
// test-plan correction 4).
//
// Temperature is a VALUE PAIR (HasTemperature + Temperature), never *float64:
// pointer-identity == comparison would make two identities with equal values
// held at different addresses compare unequal (AR-3).
//
// ReasoningDialect is part of the identity because the provider-resolved
// dialect changes the wire shape (reasoningFields emits different JSON per
// dialect); a same-name binding republish whose profile dialect differs must
// not compare equal (audit RC-2).
type PrefixIdentity struct {
	ProviderName           string
	Model                  string
	ModelGeneration        uint64
	AgentSurfaceGeneration uint64
	ReasoningLevel         string
	ReasoningDialect       string
	HasTemperature         bool
	Temperature            float64
	ToolSchemaDigest       string
	SystemPromptDigest     string
	// MemoryDigest fingerprints the rendered core-memory context frame
	// (Session.memoryContext, the user-role message at index 1). The empty
	// frame hashes deterministically like any other value.
	MemoryDigest string
}

// capturePrefixIdentityLocked snapshots the prefix identity from the same
// session state captureBindingFence reads plus the reasoning dial, the
// temperature value pair, and the two content digests. Callers must hold s.mu.
//
// INV-68-8: this runs at the four trigger events (NewSession, SwitchBinding,
// TryPublishAgentSurface, SetReasoningEffort) and at the host-side mutation
// complements that keep the cache from going stale (PublishAgentSurface,
// SetAgentSettings, RefreshPrefixIdentity, SelectModel, publishLoaded*). It is
// never reached from captureOperationTokenLocked or any per-turn
// SaveAfterTurn path; the capture counter is how the INV-68-8 test proves
// that.
func (s *Session) capturePrefixIdentityLocked() PrefixIdentity {
	s.prefixIdentityCaptures++
	// The /effort offset folds into the identity's ModelGeneration so an
	// accepted /effort flips the identity even when the effective level
	// coincides with the model default. The offset never touches
	// binding.ModelGeneration, so fencing, context-state binding revisions,
	// and surface-generation checks are unaffected.
	return PrefixIdentity{ProviderName: s.binding.ProviderName, Model: s.binding.Model, ModelGeneration: s.binding.ModelGeneration + s.prefixGeneration, AgentSurfaceGeneration: s.binding.AgentSurfaceGeneration, ReasoningLevel: string(s.effectiveReasoningLocked()), ReasoningDialect: s.reasoningDialectLocked(), HasTemperature: s.Temperature != nil, Temperature: temperatureValue(s.Temperature), ToolSchemaDigest: toolSchemaDigest(s.advertisedToolSpecs), SystemPromptDigest: systemPromptDigest(s.SystemPrompt), MemoryDigest: systemPromptDigest(s.memoryContext)}
}

// reasoningDialectLocked resolves the wire dialect the effective level will
// use against the bound provider, the same resolution ReasoningSetting
// performs. The dialect is wire-affecting, so it is part of the identity
// (audit RC-2).
func (s *Session) reasoningDialectLocked() string {
	profile := s.binding.Profile
	profile.Reasoning = s.effectiveReasoningLocked()
	return string(reasoning.Resolve(s.binding.ProviderName, config.ModelReasoning(profile)).Dialect)
}

func temperatureValue(t *float64) float64 {
	if t == nil {
		return 0
	}
	return *t
}

// bumpPrefixGenerationLocked advances the session-local prefix-generation
// offset that rides inside PrefixIdentity.ModelGeneration. SetReasoningEffort
// calls it on every accepted change because /effort alters the request body
// via reasoningFields in a way BindingFence cannot detect (gap B13); the
// offset makes the identity before/after /effort provably unequal. It never
// touches binding.ModelGeneration, so fencing, context-state binding
// revisions, and surface-generation checks are unaffected.
func (s *Session) bumpPrefixGenerationLocked() {
	s.prefixGeneration++
}

// refreshPrefixIdentityLocked caches a fresh capture. It is the ONLY writer of
// the identity cache and is reached solely from the four trigger events
// (INV-68-8).
func (s *Session) refreshPrefixIdentityLocked() {
	s.prefixIdentity = s.capturePrefixIdentityLocked()
}

// PrefixIdentity returns the cached prefix-stability identity. The cache is
// refreshed only by the four trigger events (INV-68-8); between them this
// accessor returns the same value without recomputing digests.
func (s *Session) PrefixIdentity() PrefixIdentity {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.prefixIdentity
}

// prefixResetCategories maps wire-affecting identity differences onto the
// allowlisted KindPrefixReset category names (INV-68-2). The generation
// counters never produce a category by themselves: a republish that only
// advances a counter is byte-stable and must not emit a reset. The
// surfacePublication flag distinguishes the TryPublishAgentSurface site, where
// an advanced surface generation with a changed prompt is an agent switch and
// a tool-schema change without a prompt change is a tool admission, from
// SwitchBinding, where only the binding-scoped categories apply.
func prefixResetCategories(out, in PrefixIdentity, surfacePublication bool) []string {
	var cats []string
	if out.ProviderName != in.ProviderName || out.Model != in.Model ||
		out.HasTemperature != in.HasTemperature || out.Temperature != in.Temperature {
		cats = append(cats, "model")
	}
	if out.ReasoningLevel != in.ReasoningLevel || out.ReasoningDialect != in.ReasoningDialect {
		cats = append(cats, "reasoning")
	}
	if out.ToolSchemaDigest != in.ToolSchemaDigest {
		cats = append(cats, "tools")
	}
	if out.SystemPromptDigest != in.SystemPromptDigest {
		cats = append(cats, "system_prompt")
	}
	if out.MemoryDigest != in.MemoryDigest {
		cats = append(cats, "memory")
	}
	if surfacePublication && out.AgentSurfaceGeneration != in.AgentSurfaceGeneration {
		if out.SystemPromptDigest != in.SystemPromptDigest {
			cats = append(cats, "agent_switch")
		} else if out.ToolSchemaDigest != in.ToolSchemaDigest {
			cats = append(cats, "tool_admission")
		}
	}
	return cats
}

// buildPrefixResetLocked compares the cached outgoing identity against the
// freshly captured incoming identity and, when any wire-affecting field
// changed, constructs the sealed KindPrefixReset event naming the changed
// categories (INV-68-2). It returns nil when the identities are equal on the
// wire-affecting subset, so a no-op republish emits nothing. Callers hold
// s.mu; the returned event is published after unlock.
func (s *Session) buildPrefixResetLocked(outgoing, incoming PrefixIdentity, surfacePublication bool) *events.PrefixResetEvent {
	cats := prefixResetCategories(outgoing, incoming, surfacePublication)
	if len(cats) == 0 {
		return nil
	}
	typed, err := events.NewPrefixResetEvent(events.PrefixResetEventParams{
		Categories:                cats,
		OutgoingModelGeneration:   outgoing.ModelGeneration,
		IncomingModelGeneration:   incoming.ModelGeneration,
		OutgoingSurfaceGeneration: outgoing.AgentSurfaceGeneration,
		IncomingSurfaceGeneration: incoming.AgentSurfaceGeneration,
	})
	if err != nil {
		// The categories come from the fixed allowlist above, so the sealed
		// constructor cannot reject them; drop the event rather than fail the
		// switch or publication.
		return nil
	}
	return &typed
}

// publishPrefixResetEvent delivers a sealed prefix-reset event on the bus.
// Bus.Publish is goroutine-safe and non-blocking; a nil bus is a safe no-op.
// Called after the session lock is released.
func publishPrefixResetEvent(bus *events.Bus, sessionID string, reset *events.PrefixResetEvent) {
	if bus == nil || reset == nil {
		return
	}
	ev := events.NewEvent(events.KindPrefixReset)
	ev.SessionID = sessionID
	ev.PrefixReset = reset
	bus.Publish(ev)
}
