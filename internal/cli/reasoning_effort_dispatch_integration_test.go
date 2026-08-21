package cli

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// effortProbeCompleter keeps whole requests so a test can assert the reasoning
// fields that reached the wire, not the fields a handler was configured with.
type effortProbeCompleter struct {
	name     string
	mu       sync.Mutex
	requests []provider.Request
}

func (c *effortProbeCompleter) Name() string { return c.name }

func (c *effortProbeCompleter) record(req provider.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = append(c.requests, req)
}

func (c *effortProbeCompleter) last(t *testing.T) provider.Request {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.requests) == 0 {
		t.Fatal("no provider request reached the completer")
	}
	return c.requests[len(c.requests)-1]
}

func (c *effortProbeCompleter) Chat(_ context.Context, req provider.Request) (string, error) {
	c.record(req)
	return "done", nil
}

func (c *effortProbeCompleter) ChatStream(_ context.Context, req provider.Request, w io.Writer) (string, error) {
	c.record(req)
	_, _ = io.WriteString(w, "done")
	return "done", nil
}

func (c *effortProbeCompleter) ChatTurn(_ context.Context, req provider.Request) (*provider.Response, error) {
	c.record(req)
	return &provider.Response{Content: "done", FinishReason: "stop"}, nil
}

func effortSessionProfile() config.ModelSpec {
	return config.ModelSpec{
		Name: "glm-5.2", ContextWindowTokens: 200000,
		ReasoningEfforts: []reasoning.Level{reasoning.Low, reasoning.Medium, reasoning.High},
		Reasoning:        reasoning.Low,
		ReasoningDialect: reasoning.DialectThinkingEffort,
	}
}

func effortPinnedProfile() config.ModelSpec {
	return config.ModelSpec{
		Name: "openai/o5", ContextWindowTokens: 128000,
		ReasoningEfforts: []reasoning.Level{reasoning.Low, reasoning.Medium, reasoning.High},
		Reasoning:        reasoning.Medium,
		ReasoningDialect: reasoning.DialectOpenAI,
	}
}

type effortFixture struct {
	session     *chat.Session
	sessComp    *effortProbeCompleter
	dispatcher  *runtime.Dispatcher
	definitions map[string]agents.ResolvedAgent
	routed      map[string]*effortProbeCompleter
	mu          sync.Mutex
}

func (f *effortFixture) routedCompleter(t *testing.T, providerName string) *effortProbeCompleter {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	comp, ok := f.routed[providerName]
	if !ok {
		t.Fatalf("no completer was built for provider %q", providerName)
	}
	return comp
}

// newEffortFixture wires a real session to a real dispatcher exactly as chat
// startup does, so a /effort override crosses the same seam in the test that
// it crosses in production.
func newEffortFixture(t *testing.T) *effortFixture {
	t.Helper()
	comp := &effortProbeCompleter{name: "zai"}
	sess := chat.NewSession(&config.Resolved{
		ProviderName:  "zai",
		Model:         "glm-5.2",
		Models:        []string{"glm-5.2"},
		ModelProfiles: []config.ModelSpec{effortSessionProfile()},
	}, comp)
	f := &effortFixture{
		session:     sess,
		sessComp:    comp,
		definitions: map[string]agents.ResolvedAgent{},
		routed:      map[string]*effortProbeCompleter{},
	}

	registry := agents.NewRegistry()
	for _, definition := range []agents.ResolvedAgent{
		{Name: "follower", Description: "follows the session", EffectiveTools: []string{"read_file"}},
		{Name: "pinned", Description: "pinned elsewhere", EffectiveTools: []string{"read_file"},
			Provider: "openrouter", Model: "openai/o5"},
	} {
		if err := registry.Publish(definition); err != nil {
			t.Fatal(err)
		}
		f.definitions[definition.Name] = definition
	}
	d, err := NewSessionDispatcher(SessionDispatcherOpts{
		Registry:     tools.NewDefaultRegistry(tools.DefaultOptions{}),
		Completer:    comp,
		Model:        sess.CurrentModel(),
		ProviderName: "zai",
		ModelCatalog: []config.ProviderModelGroup{
			{Provider: "zai", Selectable: true, Active: true, Models: []config.ModelSpec{effortSessionProfile()}},
			{Provider: "openrouter", Selectable: true, Models: []config.ModelSpec{effortPinnedProfile()}},
		},
		Config:           config.SubagentConfig{StoreBackend: "memory", NestedSteps: 2, SchemaRetryMax: 2},
		MaxContextTokens: sess.PromptBudget(),
		Budget:           sess.PromptBudget,
		Reasoning:        sess.ReasoningSetting,
		AgentRegistry:    registry,
		CompleterFactory: func(providerName, _ string) (provider.Completer, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			routed := &effortProbeCompleter{name: providerName}
			f.routed[providerName] = routed
			return routed, nil
		},
	})
	if err != nil {
		t.Fatalf("build dispatcher: %v", err)
	}
	t.Cleanup(d.Close)
	f.dispatcher = d
	return f
}

func (f *effortFixture) invokeSubagent(t *testing.T, name string) {
	t.Helper()
	result := f.dispatcher.Invoke(context.Background(), runtime.Request{
		ID: "effort-" + name, Kind: runtime.Subagent, Name: name,
		Input: json.RawMessage(`"nested task"`),
	})
	if result.Err != nil {
		t.Fatalf("invoke %q: %v", name, result.Err)
	}
}

func (f *effortFixture) invokeAgent(t *testing.T, name string) {
	t.Helper()
	definition, ok := f.definitions[name]
	if !ok {
		t.Fatalf("agent %q is not registered", name)
	}
	digest, err := definition.DefinitionDigest()
	if err != nil {
		t.Fatal(err)
	}
	result := f.dispatcher.Invoke(context.Background(), runtime.Request{
		ID: "effort-agent-" + name, Kind: runtime.Subagent, Name: name,
		AgentName: name, AgentDigest: digest, Input: json.RawMessage(`"nested task"`),
	})
	if result.Err != nil {
		t.Fatalf("invoke agent %q: %v", name, result.Err)
	}
}

func assertDial(t *testing.T, req provider.Request, level reasoning.Level, dialect reasoning.Dialect, what string) {
	t.Helper()
	if req.ReasoningLevel != level || req.ReasoningDialect != dialect {
		t.Fatalf("%s carried %q/%q, want %q/%q", what, req.ReasoningLevel, req.ReasoningDialect, level, dialect)
	}
}

// INV-AG-36: every request path sends the same fields for the same binding.
// A /effort override that reaches the root turn but not the nested handlers
// would make a delegated task think at a depth the operator did not choose.
func TestIntegrationEffortOverrideReachesNestedHandlers(t *testing.T) {
	f := newEffortFixture(t)
	if err := f.session.SetReasoningEffort(reasoning.High); err != nil {
		t.Fatalf("SetReasoningEffort: %v", err)
	}
	if _, err := f.session.SendUser(context.Background(), "hello", io.Discard); err != nil {
		t.Fatal(err)
	}
	assertDial(t, f.sessComp.last(t), reasoning.High, reasoning.DialectThinkingEffort, "the root turn")

	for _, name := range []string{handlerOneshot, HandlerDelegate, handlerMultiStep} {
		f.invokeSubagent(t, name)
		assertDial(t, f.sessComp.last(t), reasoning.High, reasoning.DialectThinkingEffort, "nested "+name)
	}
}

// A routed agent that follows the session tracks the session's dial; one
// pinned to another model keeps that model's configured dial, because the
// override was chosen for a model it does not run on.
func TestIntegrationEffortOverrideAndRoutedAgents(t *testing.T) {
	f := newEffortFixture(t)
	if err := f.session.SetReasoningEffort(reasoning.High); err != nil {
		t.Fatalf("SetReasoningEffort: %v", err)
	}
	f.invokeAgent(t, "follower")
	assertDial(t, f.sessComp.last(t), reasoning.High, reasoning.DialectThinkingEffort, "the session-following agent")

	f.invokeAgent(t, "pinned")
	assertDial(t, f.routedCompleter(t, "openrouter").last(t), reasoning.Medium, reasoning.DialectOpenAI, "the pinned agent")
}
