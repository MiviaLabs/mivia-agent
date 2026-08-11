package chat

import (
	"context"
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

// TestPublishAgentSurfaceEmitsPrefixResetOnToolAdmission pins that a surface
// publication with a widened registry emits exactly one event naming the
// tool-admission category (plus tools), with no prompt category because the
// prompt is unchanged.
func TestPublishAgentSurfaceEmitsPrefixResetOnToolAdmission(t *testing.T) {
	s := prefixResetSession(t)
	collect, _ := prefixResetSubscriber(t, s)

	core := tools.NewRegistry()
	core.Register(fixedBodyTool{name: "read_file"})
	if !s.TryPublishAgentSurface(AgentSurfacePublication{Prompt: "p", Registry: core}) {
		t.Fatal("core publication refused")
	}
	_ = collect() // drain the baseline event

	wider := tools.NewRegistry()
	wider.Register(fixedBodyTool{name: "read_file"})
	wider.Register(fixedBodyTool{name: "grep"})
	if !s.TryPublishAgentSurface(AgentSurfacePublication{Prompt: "p", Registry: wider}) {
		t.Fatal("tool-admission publication refused")
	}
	got := collect()
	if len(got) != 1 {
		t.Fatalf("reset events = %d, want exactly 1", len(got))
	}
	ev := got[0].PrefixReset
	if ev == nil {
		t.Fatal("bus event carried no typed payload")
	}
	if !containsCategory(ev.Categories, "tools") || !containsCategory(ev.Categories, "tool_admission") {
		t.Fatalf("categories = %v, want tools + tool_admission", ev.Categories)
	}
	for _, forbidden := range []string{"model", "reasoning", "system_prompt", "agent_switch"} {
		if containsCategory(ev.Categories, forbidden) {
			t.Fatalf("tool admission named an unrelated category %q in %v", forbidden, ev.Categories)
		}
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
