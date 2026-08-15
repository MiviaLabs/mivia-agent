package chat

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

type blockingCompleter struct {
	name  string
	start chan struct{}
	allow chan struct{}
	mu    sync.Mutex
	seen  []string
}

type requestCaptureCompleter struct {
	requests []provider.Request
}

func (c *requestCaptureCompleter) Name() string { return "capture" }
func (c *requestCaptureCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	return c.ChatStream(ctx, req, io.Discard)
}
func (c *requestCaptureCompleter) ChatStream(_ context.Context, req provider.Request, w io.Writer) (string, error) {
	c.requests = append(c.requests, req)
	_, _ = io.WriteString(w, "ok")
	return "ok", nil
}
func (c *requestCaptureCompleter) ChatTurn(_ context.Context, req provider.Request) (*provider.Response, error) {
	c.requests = append(c.requests, req)
	return &provider.Response{Content: "ok", FinishReason: "stop"}, nil
}

func TestIntegrationPlainTurnUsesPreparedPromptSnapshot(t *testing.T) {
	maxTokens := 20
	comp := &requestCaptureCompleter{}
	s := NewSession(&config.Resolved{
		ProviderName:  "p",
		Model:         "small",
		ModelProfiles: []config.ModelSpec{{Name: "small", ContextWindowTokens: 120}},
		MaxTokens:     &maxTokens,
	}, comp)
	s.Messages = []provider.Message{
		{Role: provider.RoleUser, Content: strings.Repeat("old", 160)},
		{Role: provider.RoleAssistant, Content: "old answer"},
	}
	if _, err := s.SendUser(context.Background(), "new request", io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(comp.requests) != 1 {
		t.Fatalf("provider requests = %d, want 1", len(comp.requests))
	}
	req := comp.requests[0]
	if provider.MessagesTokens(req.Messages, provider.ContextAccountingProfile{}) > s.PromptBudget() {
		t.Fatalf("request tokens = %d, budget = %d", provider.MessagesTokens(req.Messages, provider.ContextAccountingProfile{}), s.PromptBudget())
	}
	for _, message := range req.Messages {
		if strings.Contains(message.Content, "old") {
			t.Fatalf("unpruned history reached provider: %+v", req.Messages)
		}
	}
}

func TestIntegrationModelOutputCeilingCapsRequestsAndPromptBudget(t *testing.T) {
	maxTokens := 200
	comp := &requestCaptureCompleter{}
	s := NewSession(&config.Resolved{
		ProviderName: "p", Model: "small",
		ModelProfiles: []config.ModelSpec{{Name: "small", ContextWindowTokens: 1000, MaxOutputTokens: 80}},
		MaxTokens:     &maxTokens,
	}, comp)
	if got := s.PromptBudget(); got != 920 {
		t.Fatalf("prompt budget = %d, want 920", got)
	}
	if _, err := s.SendUser(context.Background(), "hello", io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(comp.requests) != 1 || comp.requests[0].MaxTokens == nil || *comp.requests[0].MaxTokens != 80 {
		t.Fatalf("request max tokens = %#v, want 80", comp.requests)
	}
}

func TestIntegrationAgentPromptBudgetDoesNotDoubleChargeOutputReserve(t *testing.T) {
	comp := &requestCaptureCompleter{}
	s := NewSession(&config.Resolved{
		ProviderName: "p", Model: "small",
		ModelProfiles: []config.ModelSpec{{Name: "small", ContextWindowTokens: 1000, MaxOutputTokens: 800}},
	}, comp)
	s.UseTools = true
	s.Tools = tools.NewRegistry()

	if _, err := s.SendUser(context.Background(), "hello", io.Discard); err != nil {
		t.Fatalf("agent turn returned %v; output reserve must not consume prompt budget", err)
	}
	if len(comp.requests) != 1 {
		t.Fatalf("provider requests = %d, want 1", len(comp.requests))
	}
	request := comp.requests[0]
	if request.MaxTokens == nil || *request.MaxTokens != 800 {
		t.Fatalf("request max tokens = %#v, want 800", request.MaxTokens)
	}
	total, err := provider.RequestTokens(request, provider.ContextAccountingProfile{})
	if err != nil {
		t.Fatal(err)
	}
	if total > 1000 {
		t.Fatalf("provider request cost = %d, exceeds model context window", total)
	}
}

func TestIntegrationAgentPreflightRejectsOversizedPrompt(t *testing.T) {
	maxTokens := 20
	comp := &requestCaptureCompleter{}
	s := NewSession(&config.Resolved{
		ProviderName:  "p",
		Model:         "small",
		ModelProfiles: []config.ModelSpec{{Name: "small", ContextWindowTokens: 100}},
		MaxTokens:     &maxTokens,
	}, comp)
	s.UseTools = true
	s.Tools = tools.NewRegistry()
	_, err := s.SendUser(context.Background(), strings.Repeat("x", 400), io.Discard)
	if !errors.Is(err, agent.ErrPromptBudgetExceeded) {
		t.Fatalf("agent preflight error = %v", err)
	}
	if len(comp.requests) != 0 {
		t.Fatalf("agent made %d provider requests before preflight", len(comp.requests))
	}
	if got := s.MessagesCount(); got != 0 {
		t.Fatalf("agent preflight mutated history: %d messages", got)
	}
}

func TestIntegrationModelBudgetTracksBindingAndRejectsOversizedPlainTurn(t *testing.T) {
	maxTokens := 20
	small := config.ModelSpec{Name: "small", ContextWindowTokens: 100}
	large := config.ModelSpec{Name: "large", ContextWindowTokens: 200}
	s := NewSession(&config.Resolved{
		ProviderName:  "p",
		Model:         "small",
		ModelProfiles: []config.ModelSpec{small},
		MaxTokens:     &maxTokens,
	}, &fakeCompleter{out: "ok"})
	if got := s.PromptBudget(); got != 80 {
		t.Fatalf("initial prompt budget = %d, want 80", got)
	}
	if _, err := s.SendUser(context.Background(), strings.Repeat("x", 400), io.Discard); !errors.Is(err, agent.ErrPromptBudgetExceeded) {
		t.Fatalf("oversized plain turn error = %v", err)
	}
	if got := s.MessagesCount(); got != 0 {
		t.Fatalf("oversized turn mutated history: %d messages", got)
	}
	if err := s.SetPromptBudget(81); err == nil {
		t.Fatal("budget above selected capacity was accepted")
	}
	if err := s.SetPromptBudget(40); err != nil {
		t.Fatal(err)
	}
	if got := s.PromptBudget(); got != 40 {
		t.Fatalf("requested prompt budget = %d, want 40", got)
	}
	if err := s.SwitchBinding(ModelBinding{ProviderName: "q", Model: "large", Completer: &fakeCompleter{out: "ok"}, Profile: large}); err != nil {
		t.Fatal(err)
	}
	if got := s.PromptBudget(); got != 40 {
		t.Fatalf("switched prompt budget = %d, want retained cap 40", got)
	}
	if err := s.SetPromptBudget(0); err != nil {
		t.Fatal(err)
	}
	if got := s.PromptBudget(); got != 180 {
		t.Fatalf("cleared prompt budget = %d, want 180", got)
	}
}

func (c *blockingCompleter) Name() string { return c.name }
func (c *blockingCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	return c.ChatStream(ctx, req, io.Discard)
}
func (c *blockingCompleter) ChatStream(ctx context.Context, req provider.Request, _ io.Writer) (string, error) {
	c.mu.Lock()
	c.seen = append(c.seen, req.Model)
	c.mu.Unlock()
	select {
	case c.start <- struct{}{}:
	default:
	}
	select {
	case <-c.allow:
		return c.name, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
func (c *blockingCompleter) ChatTurn(context.Context, provider.Request) (*provider.Response, error) {
	return &provider.Response{Content: c.name}, nil
}

func TestIntegrationModelBindingKeepsTurnGeneration(t *testing.T) {
	old := &blockingCompleter{name: "old", start: make(chan struct{}, 1), allow: make(chan struct{})}
	newComp := &blockingCompleter{name: "new", start: make(chan struct{}, 1), allow: make(chan struct{}, 1)}
	s := NewSession(&config.Resolved{ProviderName: "old-provider", Model: "old-model"}, old)
	switchErr := make(chan error, 1)
	turnDone := make(chan error, 1)
	go func() {
		_, err := s.SendUser(context.Background(), "first", io.Discard)
		turnDone <- err
	}()
	<-old.start
	switchErr <- s.SwitchBinding(ModelBinding{ProviderName: "new-provider", Model: "new-model", Completer: newComp})
	if err := <-switchErr; err == nil {
		t.Fatal("switch while turn active succeeded")
	}
	close(old.allow)
	if err := <-turnDone; err != nil {
		t.Fatal(err)
	}
	if err := s.SwitchBinding(ModelBinding{ProviderName: "new-provider", Model: "new-model", Completer: newComp}); err != nil {
		t.Fatal(err)
	}
	close(newComp.allow)
	if _, err := s.SendUser(context.Background(), "second", io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(old.seen) != 1 || old.seen[0] != "old-model" {
		t.Fatalf("old generation requests = %v", old.seen)
	}
	if len(newComp.seen) != 1 || newComp.seen[0] != "new-model" {
		t.Fatalf("new generation requests = %v", newComp.seen)
	}
	if got := s.CurrentSelection(); got.ProviderName != "new-provider" || got.Model != "new-model" {
		t.Fatalf("selection = %+v", got)
	}
}

func TestIntegrationModelBindingClosesPreviousDispatcherGeneration(t *testing.T) {
	oldDispatcher := runtime.New(runtime.Policy{})
	closed := make(chan struct{})
	oldDispatcher.OnClose(func() { close(closed) })
	newDispatcher := runtime.New(runtime.Policy{})
	s := NewSession(&config.Resolved{ProviderName: "old", Model: "one"}, &fakeCompleter{out: "ok"})
	s.SetDispatcher(oldDispatcher)
	if err := s.SwitchBinding(ModelBinding{
		ProviderName: "new",
		Model:        "two",
		Completer:    &fakeCompleter{out: "ok"},
		Dispatcher:   newDispatcher,
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("previous dispatcher generation was not closed after idle switch")
	}
}

func TestIntegrationModelBindingPublishesSkillRegistryAtomically(t *testing.T) {
	initial := skills.NewRegistry()
	next := skills.NewRegistry()
	s := NewSession(&config.Resolved{ProviderName: "old", Model: "one"}, &fakeCompleter{out: "ok"})
	s.SetBindingSkillRegistry(initial)
	if err := s.SwitchBinding(ModelBinding{
		ProviderName:  "new",
		Model:         "two",
		Completer:     &fakeCompleter{out: "ok"},
		SkillRegistry: next,
	}); err != nil {
		t.Fatal(err)
	}
	if got := s.CurrentBinding().SkillRegistry; got != next {
		t.Fatalf("skill registry = %p, want published generation %p", got, next)
	}
}

func TestIntegrationLoadBuildsBindingBeforeHistory(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileSessionStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	saved := []provider.Message{{Role: provider.RoleUser, Content: "saved"}}
	if err := store.Save("exact", saved, "new-model", "new-provider"); err != nil {
		t.Fatal(err)
	}

	old := &blockingCompleter{name: "old", allow: make(chan struct{})}
	s := NewSession(&config.Resolved{ProviderName: "old-provider", Model: "old-model"}, old)
	s.Messages = []provider.Message{{Role: provider.RoleUser, Content: "current"}}
	s.SetSessionStore(store, nil)

	factoryErr := true
	newComp := &blockingCompleter{name: "new", allow: make(chan struct{})}
	s.SetBindingFactory(func(providerName, model string) (ModelBinding, error) {
		if factoryErr {
			return ModelBinding{}, context.DeadlineExceeded
		}
		return ModelBinding{ProviderName: providerName, Model: model, Completer: newComp}, nil
	})

	if err := s.Load("exact"); err == nil {
		t.Fatal("load succeeded despite binding construction failure")
	}
	if got := s.CurrentSelection(); got.ProviderName != "old-provider" || got.Model != "old-model" {
		t.Fatalf("selection changed after failed binding: %+v", got)
	}
	if !reflect.DeepEqual(s.MessagesCopy(), []provider.Message{{Role: provider.RoleUser, Content: "current"}}) {
		t.Fatalf("history changed after failed binding: %+v", s.MessagesCopy())
	}

	factoryErr = false
	if err := s.Load("exact"); err != nil {
		t.Fatal(err)
	}
	if got := s.CurrentSelection(); got.ProviderName != "new-provider" || got.Model != "new-model" {
		t.Fatalf("selection = %+v", got)
	}
	if !reflect.DeepEqual(s.MessagesCopy(), saved) {
		t.Fatalf("history = %+v, want %+v", s.MessagesCopy(), saved)
	}
}

func TestIntegrationModelBindingRequiresFactoryForConfiguredCatalogLoad(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileSessionStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save("strict", []provider.Message{{Role: provider.RoleUser, Content: "saved"}}, "new-model", "new-provider"); err != nil {
		t.Fatal(err)
	}
	profile := config.ModelSpec{Name: "current", ContextWindowTokens: 128000}
	s := NewSession(&config.Resolved{ProviderName: "current-provider", Model: "current", ModelProfiles: []config.ModelSpec{profile}}, &blockingCompleter{name: "current", allow: make(chan struct{})})
	s.catalog = []config.ProviderModelGroup{{Provider: "current-provider", Models: []config.ModelSpec{profile}, Selectable: true}}
	s.Messages = []provider.Message{{Role: provider.RoleUser, Content: "current"}}
	s.SetSessionStore(store, nil)
	if err := s.Load("strict"); err == nil {
		t.Fatal("configured catalog loaded without a binding factory")
	}
	if got := s.MessagesCopy(); len(got) != 1 || got[0].Content != "current" {
		t.Fatalf("history changed after factory-less load: %+v", got)
	}
}

func TestModelGenerationMonotonicAcrossSuccessfulSwitches(t *testing.T) {
	comp := &blockingCompleter{name: "p", allow: make(chan struct{})}
	s := NewSession(&config.Resolved{ProviderName: "p", Model: "m", Models: []string{"m", "n"}}, comp)
	if got := s.CurrentModelGeneration(); got != 1 {
		t.Fatalf("initial generation = %d, want 1", got)
	}
	if err := s.SwitchBinding(ModelBinding{ProviderName: "p", Model: "n", Completer: comp}); err != nil {
		t.Fatal(err)
	}
	if err := s.SwitchBinding(ModelBinding{ProviderName: "p", Model: "m", Completer: comp}); err != nil {
		t.Fatal(err)
	}
	if got := s.CurrentModelGeneration(); got != 3 {
		t.Fatalf("generation after switch-back = %d, want 3", got)
	}
}
