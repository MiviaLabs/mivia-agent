package chat

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

const (
	wiringModelA = "wiring-a"
	wiringModelB = "wiring-b"
)

// wiringProfile gives every wiring model the same reasoning surface so a model
// switch changes ONLY the model category (a different default would also fire
// "reasoning" and muddy the exactly-one-category assertions).
func wiringProfile(name string) config.ModelSpec {
	return config.ModelSpec{Name: name, ContextWindowTokens: 100000, ReasoningEfforts: []reasoning.Level{reasoning.Low, reasoning.High}, Reasoning: reasoning.High, ReasoningDialect: reasoning.DialectThinkingEffort}
}

func prefixResetSession(t *testing.T) *Session {
	t.Helper()
	return NewSession(&config.Resolved{ProviderName: "zai", Model: wiringModelA, Models: []string{wiringModelA, wiringModelB}, ModelProfiles: []config.ModelSpec{wiringProfile(wiringModelA), wiringProfile(wiringModelB)}}, &requestCaptureCompleter{})
}

// prefixResetSubscriber collects KindPrefixReset events off a real bus.
func prefixResetSubscriber(t *testing.T, s *Session) (func() []events.Event, *events.Bus) {
	t.Helper()
	bus := events.New()
	var mu sync.Mutex
	var got []events.Event
	bus.Subscribe(events.KindPrefixReset, events.HandlerFunc(func(_ context.Context, ev events.Event) {
		mu.Lock()
		got = append(got, ev)
		mu.Unlock()
	}))
	s.EventBus = bus
	return func() []events.Event {
		bus.Flush()
		mu.Lock()
		defer mu.Unlock()
		out := append([]events.Event(nil), got...)
		// Drain: each collect returns only events published since the
		// previous call, so a caller that "drains the baseline event"
		// actually discards it instead of re-counting it on the next read.
		got = got[:0]
		return out
	}, bus
}

// TestSwitchBindingEmitsPrefixResetNamingChangedCategory pins INV-68-2: a
// switch to a different model emits exactly one KindPrefixReset event naming
// the changed category and carrying the outgoing/incoming generation numbers.
func TestSwitchBindingEmitsPrefixResetNamingChangedCategory(t *testing.T) {
	s := prefixResetSession(t)
	collect, _ := prefixResetSubscriber(t, s)

	binding := s.CurrentBinding()
	binding.Model = wiringModelB
	binding.Profile = wiringProfile(wiringModelB)
	if err := s.SwitchBinding(binding); err != nil {
		t.Fatalf("SwitchBinding: %v", err)
	}
	got := collect()
	if len(got) != 1 {
		t.Fatalf("reset events = %d, want exactly 1", len(got))
	}
	ev := got[0]
	if ev.Kind != events.KindPrefixReset || ev.PrefixReset == nil {
		t.Fatalf("event = %+v, want a typed KindPrefixReset", ev)
	}
	if len(ev.PrefixReset.Categories) != 1 || ev.PrefixReset.Categories[0] != "model" {
		t.Fatalf("categories = %v, want exactly [model]", ev.PrefixReset.Categories)
	}
	if ev.PrefixReset.OutgoingModelGeneration != 1 || ev.PrefixReset.IncomingModelGeneration != 2 {
		t.Fatalf("generations = %d -> %d, want 1 -> 2",
			ev.PrefixReset.OutgoingModelGeneration, ev.PrefixReset.IncomingModelGeneration)
	}
}

// TestPublishAgentSurfaceEmitsPrefixResetOnAgentSwitch pins that a surface
// publication with a changed system prompt emits exactly one event naming the
// agent-switch category (plus system_prompt), with no tool category because
// the tool schema is unchanged.
func TestPublishAgentSurfaceEmitsPrefixResetOnAgentSwitch(t *testing.T) {
	s := prefixResetSession(t)
	collect, _ := prefixResetSubscriber(t, s)

	base := tools.NewRegistry()
	base.Register(fixedBodyTool{name: "read_file"})
	if !s.TryPublishAgentSurface(AgentSurfacePublication{Prompt: "you are the old agent", Registry: base}) {
		t.Fatal("baseline publication refused")
	}
	_ = collect() // drain the baseline event (nil surface -> old agent)

	next := tools.NewRegistry()
	next.Register(fixedBodyTool{name: "read_file"})
	if !s.TryPublishAgentSurface(AgentSurfacePublication{Prompt: "you are the NEW agent", Registry: next}) {
		t.Fatal("agent-switch publication refused")
	}
	got := collect()
	if len(got) != 1 {
		t.Fatalf("reset events = %d, want exactly 1", len(got))
	}
	ev := got[0].PrefixReset
	if ev == nil {
		t.Fatal("bus event carried no typed payload")
	}
	if !containsCategory(ev.Categories, "system_prompt") || !containsCategory(ev.Categories, "agent_switch") {
		t.Fatalf("categories = %v, want system_prompt + agent_switch", ev.Categories)
	}
	for _, forbidden := range []string{"model", "reasoning", "tools", "tool_admission"} {
		if containsCategory(ev.Categories, forbidden) {
			t.Fatalf("agent switch named an unrelated category %q in %v", forbidden, ev.Categories)
		}
	}
}

// TestToolAdmissionPublicationLeavesThePrefixIdentityUnchanged pins the core
// invariant of plan tools-advertising/01: TryPublishAgentSurface (the
// admission-widening path load_tools and its step-boundary publication use)
// changes execution authority (Registry, Dispatcher) but never
// AdvertisedToolSpecs. The prefix identity's tool digest is derived from the
// pinned advertised snapshot, not the live registry, so widening the
// execution registry alone must emit NO reset - a mid-turn load_tools
// admission must never change the wire tools[] array, or the provider's
// implicit prompt-cache prefix would invalidate from token 0 on every
// admission.
func TestToolAdmissionPublicationLeavesThePrefixIdentityUnchanged(t *testing.T) {
	s := prefixResetSession(t)
	collect, _ := prefixResetSubscriber(t, s)

	core := tools.NewRegistry()
	core.Register(fixedBodyTool{name: "read_file"})
	// Only PublishAgentSurface (attach / /agent / /model) may pin a snapshot;
	// establish the baseline through it, exactly like production does.
	s.PublishAgentSurface("p", 0, core, nil, nil, "", core.OpenAITools())
	_ = collect() // drain the baseline event
	before := s.AdvertisedToolSpecs()

	wider := tools.NewRegistry()
	wider.Register(fixedBodyTool{name: "read_file"})
	wider.Register(fixedBodyTool{name: "grep"})
	// A real admission-widening call never sets AdvertisedToolSpecs - only
	// PublishAgentSurface (attach / /agent / /model) may pin a new snapshot.
	if !s.TryPublishAgentSurface(AgentSurfacePublication{Prompt: "p", Registry: wider}) {
		t.Fatal("tool-admission publication refused")
	}
	got := collect()
	if len(got) != 0 {
		t.Fatalf("reset events = %d, want 0: admission publication must not change the advertised tools[] array", len(got))
	}
	after := s.AdvertisedToolSpecs()
	if fmt.Sprintf("%v", before) != fmt.Sprintf("%v", after) {
		t.Fatalf("advertised snapshot changed across admission: before=%v after=%v", before, after)
	}
}

// TestPrefixResetNotEmittedWhenIdentitiesEqual pins INV-68-2's no-false-reset
// half: a no-op republish whose wire-affecting identity is unchanged emits
// nothing, and a refused switch / refused surface publication emits nothing and
// leaves the cached identity untouched.
func TestPrefixResetNotEmittedWhenIdentitiesEqual(t *testing.T) {
	s := prefixResetSession(t)
	collect, _ := prefixResetSubscriber(t, s)

	// No-op republish: same model, same reasoning surface, same tools/prompt.
	binding := s.CurrentBinding()
	binding.Profile = wiringProfile(wiringModelA)
	if err := s.SwitchBinding(binding); err != nil {
		t.Fatalf("no-op republish: %v", err)
	}
	if got := collect(); len(got) != 0 {
		t.Fatalf("no-op republish emitted %d reset events", len(got))
	}

	// Refused switch: active work blocks the publish before any change.
	s.mu.Lock()
	s.activeTurns = 1
	s.mu.Unlock()
	before := s.PrefixIdentity()
	binding = s.CurrentBinding()
	binding.Model = wiringModelB
	binding.Profile = wiringProfile(wiringModelB)
	if err := s.SwitchBinding(binding); err == nil {
		t.Fatal("switch while work is active must be refused")
	}
	if after := s.PrefixIdentity(); after != before {
		t.Fatal("a refused switch changed the cached identity")
	}
	if got := collect(); len(got) != 0 {
		t.Fatalf("refused switch emitted %d reset events", len(got))
	}

	// Refused surface publication: a failing precondition returns false.
	s.mu.Lock()
	s.activeTurns = 0
	s.mu.Unlock()
	before = s.PrefixIdentity()
	if s.TryPublishAgentSurface(AgentSurfacePublication{Prompt: "x", RequireTurnID: 999}) {
		t.Fatal("publication with a failing precondition must be refused")
	}
	if after := s.PrefixIdentity(); after != before {
		t.Fatal("a refused publication changed the cached identity")
	}
	if got := collect(); len(got) != 0 {
		t.Fatalf("refused publication emitted %d reset events", len(got))
	}
}

// TestPrefixResetBusPublish pins the delivery contract: with a subscribed bus
// the event arrives after the call returns; a nil EventBus is a safe no-op.
func TestPrefixResetBusPublish(t *testing.T) {
	s := prefixResetSession(t)
	collect, _ := prefixResetSubscriber(t, s)
	binding := s.CurrentBinding()
	binding.Model = wiringModelB
	binding.Profile = wiringProfile(wiringModelB)
	if err := s.SwitchBinding(binding); err != nil {
		t.Fatalf("SwitchBinding: %v", err)
	}
	got := collect()
	if len(got) != 1 || got[0].PrefixReset == nil {
		t.Fatalf("events = %+v, want one typed reset event after the call", got)
	}

	// A nil EventBus is a safe no-op: the switch still succeeds.
	s2 := prefixResetSession(t)
	binding2 := s2.CurrentBinding()
	binding2.Model = wiringModelB
	binding2.Profile = wiringProfile(wiringModelB)
	if err := s2.SwitchBinding(binding2); err != nil {
		t.Fatalf("switch without a bus: %v", err)
	}
}

func containsCategory(cats []string, want string) bool {
	for _, c := range cats {
		if c == want {
			return true
		}
	}
	return false
}

// TestPublishAgentSurfaceRecapturesIdentity pins audit RC-1: the host-side
// /agent publication path (PublishAgentSurface) must recapture the identity and
// emit one reset, and the cache must stay fresh so the NEXT tool admission does
// not re-report the earlier prompt change as a false system_prompt/agent_switch
// reset.
func TestPublishAgentSurfaceRecapturesIdentity(t *testing.T) {
	s := prefixResetSession(t)
	collect, _ := prefixResetSubscriber(t, s)

	reg := tools.NewRegistry()
	reg.Register(fixedBodyTool{name: "read_file"})
	s.PublishAgentSurface("you are the NEW agent", 4, reg, nil, nil, "", reg.OpenAITools())
	got := collect()
	if len(got) != 1 || got[0].PrefixReset == nil {
		t.Fatalf("PublishAgentSurface events = %d, want exactly 1 typed reset", len(got))
	}
	if !containsCategory(got[0].PrefixReset.Categories, "system_prompt") || !containsCategory(got[0].PrefixReset.Categories, "agent_switch") {
		t.Fatalf("categories = %v, want system_prompt + agent_switch", got[0].PrefixReset.Categories)
	}

	// A pure tool admission right after must NOT re-report the prompt, AND
	// (plan tools-advertising/01) must emit no reset at all: it never touches
	// AdvertisedToolSpecs, so the wire tools[] array - and therefore the
	// prefix identity - is unchanged by construction.
	wider := tools.NewRegistry()
	wider.Register(fixedBodyTool{name: "read_file"})
	wider.Register(fixedBodyTool{name: "grep"})
	if !s.TryPublishAgentSurface(AgentSurfacePublication{Prompt: "you are the NEW agent", Registry: wider}) {
		t.Fatal("tool admission refused")
	}
	got = collect()
	if len(got) != 0 {
		t.Fatalf("tool admission events = %d, want 0: admission must not change the pinned advertised snapshot", len(got))
	}
}

// TestSetReasoningEffortEmitsPrefixReset pins audit RC-3: an accepted /effort
// change is wire-affecting and must emit exactly one reset naming "reasoning";
// the clear path emits one too; a same-level no-op emits nothing.
func TestSetReasoningEffortEmitsPrefixReset(t *testing.T) {
	s := prefixResetSession(t)
	collect, _ := prefixResetSubscriber(t, s)

	if err := s.SetReasoningEffort(reasoning.Low); err != nil {
		t.Fatalf("SetReasoningEffort(Low): %v", err)
	}
	got := collect()
	if len(got) != 1 || got[0].PrefixReset == nil {
		t.Fatalf("effort-change events = %d, want exactly 1", len(got))
	}
	if !containsCategory(got[0].PrefixReset.Categories, "reasoning") {
		t.Fatalf("categories = %v, want reasoning", got[0].PrefixReset.Categories)
	}

	if err := s.SetReasoningEffort(""); err != nil {
		t.Fatalf("clear effort: %v", err)
	}
	got = collect()
	if len(got) != 1 || got[0].PrefixReset == nil || !containsCategory(got[0].PrefixReset.Categories, "reasoning") {
		t.Fatalf("clear path events = %v, want exactly 1 reasoning reset", got)
	}

	// High is already effective after the clear: setting it is a no-op on the
	// wire-affecting subset, so nothing may be emitted (INV-68-2).
	if err := s.SetReasoningEffort(reasoning.High); err != nil {
		t.Fatalf("same-level effort: %v", err)
	}
	if got := collect(); len(got) != 0 {
		t.Fatalf("same-level effort emitted %d reset events, want 0", len(got))
	}
}

// TestPrefixIdentityIncludesReasoningDialect pins audit RC-2: a same-name
// binding republish whose provider-resolved dialect differs changes the wire
// reasoning shape, so the identity must differ and a reset must name
// "reasoning".
func TestPrefixIdentityIncludesReasoningDialect(t *testing.T) {
	s := prefixResetSession(t)
	collect, _ := prefixResetSubscriber(t, s)

	before := s.PrefixIdentity()
	binding := s.PublishedBinding()
	binding.Profile.ReasoningDialect = reasoning.DialectThinkingPreserved
	if err := s.SwitchBinding(binding); err != nil {
		t.Fatalf("dialect republish: %v", err)
	}
	after := s.PrefixIdentity()
	if after == before {
		t.Fatal("identity unchanged though the wire reasoning dialect changed")
	}
	if after.ReasoningDialect != string(reasoning.DialectThinkingPreserved) {
		t.Fatalf("dialect = %q, want %q", after.ReasoningDialect, reasoning.DialectThinkingPreserved)
	}
	got := collect()
	if len(got) != 1 || got[0].PrefixReset == nil || !containsCategory(got[0].PrefixReset.Categories, "reasoning") {
		t.Fatalf("dialect republish events = %v, want exactly 1 reasoning reset", got)
	}
}

// TestSelectModelEmitsPrefixReset pins audit RC-1: SelectModel renames the
// selection, which is wire-affecting; it must emit exactly one reset naming
// "model" and leave the cache fresh.
func TestSelectModelEmitsPrefixReset(t *testing.T) {
	s := prefixResetSession(t)
	collect, _ := prefixResetSubscriber(t, s)

	if !s.SelectModel(wiringModelB) {
		t.Fatal("SelectModel refused")
	}
	got := collect()
	if len(got) != 1 || got[0].PrefixReset == nil {
		t.Fatalf("SelectModel events = %d, want exactly 1", len(got))
	}
	if !containsCategory(got[0].PrefixReset.Categories, "model") {
		t.Fatalf("categories = %v, want model", got[0].PrefixReset.Categories)
	}
}

// TestRefreshPrefixIdentityAfterToolSurfaceWrite pins audit RC-1's attach
// path: a direct sess.Tools write (CLI attach) followed by RefreshPrefixIdentity
// emits a tools reset, and the fresh cache makes the following no-op republish
// silent instead of a false reset.
func TestRefreshPrefixIdentityAfterToolSurfaceWrite(t *testing.T) {
	s := prefixResetSession(t)
	collect, _ := prefixResetSubscriber(t, s)

	reg := tools.NewRegistry()
	reg.Register(fixedBodyTool{name: "read_file"})
	s.Tools = reg
	// Mirrors the real attach path (scopeAttachedToolSurface): the advertised
	// snapshot is pinned alongside the direct registry write, since
	// RefreshPrefixIdentity reads the pinned snapshot, not the live registry
	// (plan tools-advertising/01).
	s.SetAdvertisedToolSpecs(reg.OpenAITools())
	s.RefreshPrefixIdentity()
	got := collect()
	if len(got) != 1 || got[0].PrefixReset == nil || !containsCategory(got[0].PrefixReset.Categories, "tools") {
		t.Fatalf("attach refresh events = %v, want exactly 1 tools reset", got)
	}

	binding := s.CurrentBinding()
	binding.Profile = wiringProfile(wiringModelA)
	if err := s.SwitchBinding(binding); err != nil {
		t.Fatalf("no-op republish: %v", err)
	}
	if got := collect(); len(got) != 0 {
		t.Fatalf("no-op republish after refresh emitted %d events, want 0", len(got))
	}
}

// TestMemoryChangeEmitsPrefixResetWithMemoryCategory pins the INV-68-1
// repair: a memory promotion rewrites the user-role frame at index 1, which
// changes wire bytes without touching the system prompt, so the identity
// carries a MemoryDigest and the reset names "memory" - and ONLY "memory"
// when the prompt, tools, model, and reasoning are unchanged.
func TestMemoryChangeEmitsPrefixResetWithMemoryCategory(t *testing.T) {
	s := prefixResetSession(t)
	collect, _ := prefixResetSubscriber(t, s)

	s.SetAgentSettings("stable prompt", 4, "- fact one")
	_ = collect() // drain the baseline event

	s.SetAgentSettings("stable prompt", 4, "- fact one\n- fact two")
	got := collect()
	if len(got) != 1 {
		t.Fatalf("reset events = %d, want exactly 1", len(got))
	}
	ev := got[0].PrefixReset
	if ev == nil {
		t.Fatal("bus event carried no typed payload")
	}
	if !containsCategory(ev.Categories, "memory") {
		t.Fatalf("categories = %v, want memory", ev.Categories)
	}
	for _, forbidden := range []string{"model", "reasoning", "tools", "system_prompt", "agent_switch", "tool_admission"} {
		if containsCategory(ev.Categories, forbidden) {
			t.Fatalf("memory change named an unrelated category %q in %v", forbidden, ev.Categories)
		}
	}

	// An unchanged memory block with an unchanged prompt emits nothing.
	s.SetAgentSettings("stable prompt", 4, "- fact one\n- fact two")
	if got := collect(); len(got) != 0 {
		t.Fatalf("no-op settings write emitted %d reset events, want 0", len(got))
	}

	// Clearing the block also changes the wire bytes: memory again.
	s.SetAgentSettings("stable prompt", 4, "")
	got = collect()
	if len(got) != 1 || got[0].PrefixReset == nil || !containsCategory(got[0].PrefixReset.Categories, "memory") {
		t.Fatalf("clearing the memory block did not emit a memory reset: %+v", got)
	}
	if containsCategory(got[0].PrefixReset.Categories, "system_prompt") {
		t.Fatalf("unchanged prompt reported system_prompt: %v", got[0].PrefixReset.Categories)
	}
}
