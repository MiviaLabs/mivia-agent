package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// Phase 2 of the agent model routing plan: every routed agent executes on a
// completer bound to its own resolved provider, never on the session's
// completer by accident.

// bindingProbeCompleter records the (provider, model) each turn actually ran
// against, so a test can assert what reached the wire rather than what was
// configured.
type bindingProbeCompleter struct {
	name string
	mu   sync.Mutex
	seen []string
}

func (c *bindingProbeCompleter) Name() string { return c.name }

func (c *bindingProbeCompleter) record(req provider.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seen = append(c.seen, c.name+"/"+req.Model)
}

func (c *bindingProbeCompleter) calls() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.seen...)
}

func (c *bindingProbeCompleter) Chat(_ context.Context, req provider.Request) (string, error) {
	c.record(req)
	return "done", nil
}

func (c *bindingProbeCompleter) ChatStream(_ context.Context, req provider.Request, _ io.Writer) (string, error) {
	c.record(req)
	return "done", nil
}

func (c *bindingProbeCompleter) ChatTurn(_ context.Context, req provider.Request) (*provider.Response, error) {
	c.record(req)
	return &provider.Response{Content: "done", FinishReason: "stop"}, nil
}

// bindingTestCatalog declares two providers so a routed agent can select one
// the session is not using.
func bindingTestCatalog() []config.ProviderModelGroup {
	return []config.ProviderModelGroup{
		{Provider: "zai", Selectable: true, Active: true, Models: []config.ModelSpec{
			{Name: "glm-5.2", ContextWindowTokens: 200000},
		}},
		{Provider: "deepseek", Selectable: true, Models: []config.ModelSpec{
			{Name: "deepseek-v4-flash", ContextWindowTokens: 64000},
		}},
	}
}

type bindingFixture struct {
	dispatcher *runtime.Dispatcher
	session    *bindingProbeCompleter
	built      map[string]*bindingProbeCompleter
	factoryErr error
	mu         sync.Mutex
}

// newBindingFixture builds a session dispatcher whose session binding is
// zai/glm-5.2, with a completer factory that mints one probe per provider.
func newBindingFixture(t *testing.T, definitions []agents.ResolvedAgent, opts ...func(*SessionDispatcherOpts)) *bindingFixture {
	t.Helper()
	reg := agents.NewRegistry()
	for _, d := range definitions {
		if err := reg.Publish(d); err != nil {
			t.Fatal(err)
		}
	}
	f := &bindingFixture{
		session: &bindingProbeCompleter{name: "zai"},
		built:   map[string]*bindingProbeCompleter{},
	}
	o := SessionDispatcherOpts{
		Registry:         tools.NewDefaultRegistry(tools.DefaultOptions{}),
		Completer:        f.session,
		Model:            "glm-5.2",
		ProviderName:     "zai",
		ModelCatalog:     bindingTestCatalog(),
		Config:           config.DefaultSubagentConfig,
		MaxContextTokens: 200000,
		AgentRegistry:    reg,
		CompleterFactory: func(providerName, _ string) (provider.Completer, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.factoryErr != nil {
				return nil, f.factoryErr
			}
			c := &bindingProbeCompleter{name: providerName}
			f.built[providerName] = c
			return c, nil
		},
	}
	for _, apply := range opts {
		apply(&o)
	}
	d, err := NewSessionDispatcher(o)
	if err != nil {
		t.Fatalf("build dispatcher: %v", err)
	}
	t.Cleanup(d.Close)
	f.dispatcher = d
	return f
}

func invokeAgent(t *testing.T, f *bindingFixture, definition agents.ResolvedAgent, id string) error {
	t.Helper()
	digest, err := definition.DefinitionDigest()
	if err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal("do the work")
	return f.dispatcher.Invoke(context.Background(), runtime.Request{
		ID: id, Kind: runtime.Subagent, Name: definition.Name,
		AgentName: definition.Name, AgentDigest: digest, Input: input,
	}).Err
}

// The core contract: an agent bound to deepseek must not run on the session's
// zai completer.
func TestRoutedAgentUsesItsOwnProviderCompleter(t *testing.T) {
	bound := agents.ResolvedAgent{
		Name: "bound", Description: "d", EffectiveTools: []string{"read_file"},
		Provider: "deepseek", Model: "deepseek-v4-flash",
	}
	f := newBindingFixture(t, []agents.ResolvedAgent{bound})
	if err := invokeAgent(t, f, bound, "r1"); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if got := f.session.calls(); len(got) != 0 {
		t.Fatalf("routed agent leaked onto the session completer: %v", got)
	}
	built := f.built["deepseek"]
	if built == nil {
		t.Fatal("no completer was built for the agent's provider")
	}
	calls := built.calls()
	if len(calls) == 0 || calls[0] != "deepseek/deepseek-v4-flash" {
		t.Fatalf("routed calls = %v, want deepseek/deepseek-v4-flash", calls)
	}
}

// An agent that declares no binding keeps today's behaviour exactly: session
// completer, session model, no factory call.
func TestUnboundAgentStillUsesSessionCompleter(t *testing.T) {
	plain := agents.ResolvedAgent{
		Name: "plain", Description: "d", EffectiveTools: []string{"read_file"},
	}
	f := newBindingFixture(t, []agents.ResolvedAgent{plain})
	if err := invokeAgent(t, f, plain, "r1"); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	calls := f.session.calls()
	if len(calls) == 0 || calls[0] != "zai/glm-5.2" {
		t.Fatalf("session calls = %v, want zai/glm-5.2", calls)
	}
	if len(f.built) != 0 {
		t.Fatalf("no completer should be built for an unbound agent, got %v", f.built)
	}
}

// A model-only override stays on the session provider: completers are
// provider-scoped and carry the model per request.
func TestModelOnlyOverrideReusesSessionCompleter(t *testing.T) {
	local := agents.ResolvedAgent{
		Name: "local", Description: "d", EffectiveTools: []string{"read_file"},
		Model: "glm-5.2",
	}
	f := newBindingFixture(t, []agents.ResolvedAgent{local})
	if err := invokeAgent(t, f, local, "r1"); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if len(f.built) != 0 {
		t.Fatalf("model-only override must not build a new completer, got %v", f.built)
	}
	if calls := f.session.calls(); len(calls) == 0 || calls[0] != "zai/glm-5.2" {
		t.Fatalf("session calls = %v", calls)
	}
}

// Fail closed: a declared binding the catalog does not offer must never run.
func TestUnselectableBindingFailsClosed(t *testing.T) {
	bad := agents.ResolvedAgent{
		Name: "bad", Description: "d", EffectiveTools: []string{"read_file"},
		Provider: "deepseek", Model: "no-such-model",
	}
	f := newBindingFixture(t, []agents.ResolvedAgent{bad})
	err := invokeAgent(t, f, bad, "r1")
	if err == nil || !strings.Contains(err.Error(), "no-such-model") {
		t.Fatalf("unselectable binding must fail closed with an actionable error, got %v", err)
	}
	if len(f.session.calls()) != 0 {
		t.Fatal("a rejected binding must not fall back to the session completer")
	}
}

// An empty catalog is not authorization. Without a catalog nothing can vouch
// for a declared binding, so it must fail rather than silently pass.
func TestDeclaredBindingWithEmptyCatalogFailsClosed(t *testing.T) {
	bound := agents.ResolvedAgent{
		Name: "bound", Description: "d", EffectiveTools: []string{"read_file"},
		Provider: "deepseek", Model: "deepseek-v4-flash",
	}
	f := newBindingFixture(t, []agents.ResolvedAgent{bound}, func(o *SessionDispatcherOpts) {
		o.ModelCatalog = nil
	})
	if err := invokeAgent(t, f, bound, "r1"); err == nil {
		t.Fatal("a declared binding with no catalog to validate against must fail closed")
	}
	if len(f.session.calls()) != 0 {
		t.Fatal("a rejected binding must not fall back to the session completer")
	}
}

// A missing factory is a hard error for a foreign provider, never a silent
// downgrade to the session completer - that downgrade is the bug this phase
// exists to remove.
func TestForeignProviderWithoutFactoryFailsClosed(t *testing.T) {
	bound := agents.ResolvedAgent{
		Name: "bound", Description: "d", EffectiveTools: []string{"read_file"},
		Provider: "deepseek", Model: "deepseek-v4-flash",
	}
	f := newBindingFixture(t, []agents.ResolvedAgent{bound}, func(o *SessionDispatcherOpts) {
		o.CompleterFactory = nil
	})
	err := invokeAgent(t, f, bound, "r1")
	if err == nil || !strings.Contains(err.Error(), "deepseek") {
		t.Fatalf("want actionable provider error, got %v", err)
	}
	if len(f.session.calls()) != 0 {
		t.Fatal("must not fall back to the session completer")
	}
}

// Two agents on different providers in one dispatcher must not contaminate
// each other. Run under -race to cover the shared-handler path.
func TestParallelAgentsKeepSeparateBindings(t *testing.T) {
	foreign := agents.ResolvedAgent{
		Name: "foreign", Description: "d", EffectiveTools: []string{"read_file"},
		Provider: "deepseek", Model: "deepseek-v4-flash",
	}
	local := agents.ResolvedAgent{
		Name: "local", Description: "d", EffectiveTools: []string{"read_file"},
	}
	f := newBindingFixture(t, []agents.ResolvedAgent{foreign, local})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		definition := local
		if i%2 == 0 {
			definition = foreign
		}
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			if err := invokeAgent(t, f, definition, fmt.Sprintf("par-%d", id)); err != nil {
				t.Errorf("invoke %s: %v", definition.Name, err)
			}
		}(i)
	}
	wg.Wait()
	for _, call := range f.session.calls() {
		if call != "zai/glm-5.2" {
			t.Fatalf("session completer saw a foreign binding: %s", call)
		}
	}
	built := f.built["deepseek"]
	if built == nil {
		t.Fatal("foreign provider completer missing")
	}
	for _, call := range built.calls() {
		if call != "deepseek/deepseek-v4-flash" {
			t.Fatalf("foreign completer saw %s", call)
		}
	}
}

// A routed agent on a smaller-window model must not inherit the session
// model's larger prompt budget, or local pruning is replaced by provider-side
// context-overflow errors.
func TestRoutedBindingClampsContextBudget(t *testing.T) {
	opts := SessionDispatcherOpts{
		Model: "glm-5.2", ProviderName: "zai",
		ModelCatalog:     bindingTestCatalog(),
		MaxContextTokens: 200000,
		Budget:           func() int { return 200000 },
		Completer:        &bindingProbeCompleter{name: "zai"},
		CompleterFactory: func(providerName, _ string) (provider.Completer, error) {
			return &bindingProbeCompleter{name: providerName}, nil
		},
	}
	foreign := agents.ResolvedAgent{
		Name: "foreign", Provider: "deepseek", Model: "deepseek-v4-flash",
	}
	binding, err := resolveAgentBinding(foreign, opts)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := binding.contextBudget(); got != 64000 {
		t.Fatalf("routed context budget = %d, want the routed model's 64000 window", got)
	}

	// The session's own binding keeps the session budget untouched.
	plain, err := resolveAgentBinding(agents.ResolvedAgent{Name: "plain"}, opts)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := plain.contextBudget(); got != 200000 {
		t.Fatalf("unbound context budget = %d, want the session's 200000", got)
	}
}

// The live session budget (/budget) still applies to a routed agent when it is
// the tighter of the two.
func TestRoutedBindingHonoursTighterLiveBudget(t *testing.T) {
	opts := SessionDispatcherOpts{
		Model: "glm-5.2", ProviderName: "zai",
		ModelCatalog:     bindingTestCatalog(),
		MaxContextTokens: 200000,
		Budget:           func() int { return 8000 },
		Completer:        &bindingProbeCompleter{name: "zai"},
		CompleterFactory: func(providerName, _ string) (provider.Completer, error) {
			return &bindingProbeCompleter{name: providerName}, nil
		},
	}
	binding, err := resolveAgentBinding(agents.ResolvedAgent{
		Name: "foreign", Provider: "deepseek", Model: "deepseek-v4-flash",
	}, opts)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := binding.contextBudget(); got != 8000 {
		t.Fatalf("context budget = %d, want the tighter live session budget 8000", got)
	}
}

// Phase 3: turn count and resource ceilings are independent. An agent with
// unlimited turns must still be bounded in wall-clock time and spend.

func TestAgentWallClockCeilingStopsUnlimitedTurns(t *testing.T) {
	unlimited := 0
	one := 1
	slow := agents.ResolvedAgent{
		Name: "slow", Description: "d", EffectiveTools: []string{"read_file"},
		MaxTurns: &unlimited, TimeoutSeconds: &one,
	}
	f := newBindingFixture(t, []agents.ResolvedAgent{slow}, func(o *SessionDispatcherOpts) {
		o.Completer = &blockingCompleter{}
	})
	err := invokeAgent(t, f, slow, "slow-1")
	if err == nil {
		t.Fatal("an agent past its wall-clock ceiling must not report success")
	}
	if !errors.Is(err, ErrAgentWallClockExceeded) {
		t.Fatalf("exhaustion must carry the typed cause, got %v", err)
	}
	if !strings.Contains(err.Error(), "slow") {
		t.Fatalf("error must name the agent, got %v", err)
	}
}

// An agent ceiling may lower the operator's cap but never raise it.
func TestTokenCeilingTakesTheTighterOfAgentAndSession(t *testing.T) {
	sessionCap := 4096
	for name, tc := range map[string]struct {
		agentTokens *int
		want        int
	}{
		"agent lowers":        {agentTokens: intPtr(1024), want: 1024},
		"agent cannot raise":  {agentTokens: intPtr(99999), want: 4096},
		"agent declares none": {agentTokens: nil, want: 4096},
	} {
		t.Run(name, func(t *testing.T) {
			binding, err := resolveAgentBinding(
				agents.ResolvedAgent{Name: "a", MaxTokens: tc.agentTokens},
				SessionDispatcherOpts{Model: "glm-5.2", ProviderName: "zai", MaxTokens: &sessionCap},
			)
			if err != nil {
				t.Fatal(err)
			}
			if binding.maxTokens != tc.want {
				t.Fatalf("maxTokens = %d, want %d", binding.maxTokens, tc.want)
			}
		})
	}
}

// The agent ceiling layers over the caller's deadline instead of replacing it,
// so a generous agent policy can never loosen a tight task timeout.
func TestWallClockCeilingNeverLoosensATighterParent(t *testing.T) {
	binding := agentBinding{wallClock: time.Hour}
	parent, cancelParent := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelParent()
	ctx, cancel := binding.withWallClock(parent)
	defer cancel()
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("a tighter parent deadline must still bound the agent")
	}
}

// blockingCompleter never returns until its context is cancelled, so a
// wall-clock ceiling is the only thing that can end the turn.
type blockingCompleter struct{}

func (blockingCompleter) Name() string { return "zai" }
func (blockingCompleter) Chat(ctx context.Context, _ provider.Request) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}
func (blockingCompleter) ChatStream(ctx context.Context, _ provider.Request, _ io.Writer) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}
func (blockingCompleter) ChatTurn(ctx context.Context, _ provider.Request) (*provider.Response, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
