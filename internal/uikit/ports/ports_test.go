package ports

import (
	"context"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/intent"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// fakeTurnHandle and fakeConversation are minimal fakes proving the
// interfaces are actually implementable in the shape the session adapter
// (a later phase) will need: Events() must be directly selectable, Send
// must thread a context and an intent.Send through to a TurnHandle.
type fakeTurnHandle struct {
	id     string
	events chan uievent.Event
}

func (h *fakeTurnHandle) ID() string                   { return h.id }
func (h *fakeTurnHandle) Events() <-chan uievent.Event { return h.events }
func (h *fakeTurnHandle) Cancel()                      { close(h.events) }

type fakeConversation struct {
	history []Message
	model   ModelInfo
	usage   Usage
}

func (c *fakeConversation) Send(ctx context.Context, in intent.Send) (TurnHandle, error) {
	events := make(chan uievent.Event, 1)
	events <- uievent.Event{Kind: uievent.KindTurnStart, Body: uievent.TurnStartBody{Input: in.Text}}
	close(events)
	return &fakeTurnHandle{id: "t1", events: events}, nil
}
func (c *fakeConversation) History() []Message  { return c.history }
func (c *fakeConversation) Model() ModelInfo    { return c.model }
func (c *fakeConversation) ContextUsage() Usage { return c.usage }
func (c *fakeConversation) Title() string       { return "fake title" }
func (c *fakeConversation) ID() string          { return "fake-conv" }

var _ Conversation = (*fakeConversation)(nil)
var _ TurnHandle = (*fakeTurnHandle)(nil)

func TestConversationSendReturnsTurnHandle(t *testing.T) {
	conv := &fakeConversation{
		history: []Message{{Role: "user", Text: "hi", At: time.Now()}},
		model:   ModelInfo{Name: "test-model", Provider: "test"},
		usage:   Usage{InputTokens: 10, OutputTokens: 20, CachedTokens: 1, CostUSD: 0.01},
	}
	handle, err := conv.Send(context.Background(), intent.Send{Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if handle.ID() != "t1" {
		t.Errorf("handle.ID() = %q, want t1", handle.ID())
	}
	ev, ok := <-handle.Events()
	if !ok {
		t.Fatal("expected one event before channel closes")
	}
	body, ok := ev.Body.(uievent.TurnStartBody)
	if !ok || body.Input != "hello" {
		t.Errorf("unexpected event body: %+v", ev.Body)
	}
	if _, ok := <-handle.Events(); ok {
		t.Fatal("expected channel to be closed after one event")
	}

	if len(conv.History()) != 1 {
		t.Errorf("History() length = %d, want 1", len(conv.History()))
	}
	if conv.Model().Name != "test-model" {
		t.Errorf("Model().Name = %q, want test-model", conv.Model().Name)
	}
	if conv.ContextUsage().InputTokens != 10 {
		t.Errorf("ContextUsage().InputTokens = %d, want 10", conv.ContextUsage().InputTokens)
	}
}

func TestTurnHandleCancelClosesEvents(t *testing.T) {
	h := &fakeTurnHandle{id: "t2", events: make(chan uievent.Event)}
	h.Cancel()
	if _, ok := <-h.Events(); ok {
		t.Fatal("expected Events() channel closed after Cancel()")
	}
}

// fakeApprover and fakeSessionStore prove Approver/SessionStore are
// implementable with the ID-addressable, multi-outstanding shape ports.go
// documents (distinct from the SDK's single-shot channel.Notifier).
type fakeApprover struct {
	pending  chan ApprovalRequest
	resolved map[string]Decision
}

func (a *fakeApprover) Pending() <-chan ApprovalRequest { return a.pending }
func (a *fakeApprover) Resolve(id string, decision Decision) {
	if a.resolved == nil {
		a.resolved = map[string]Decision{}
	}
	a.resolved[id] = decision
}

var _ Approver = (*fakeApprover)(nil)

func TestApproverResolve(t *testing.T) {
	a := &fakeApprover{pending: make(chan ApprovalRequest, 1)}
	req := ApprovalRequest{ID: "a1", ToolName: "edit", TurnID: "t1", Args: map[string]any{"path": "x"}}
	a.pending <- req
	got := <-a.Pending()
	if got.ID != "a1" || got.ToolName != "edit" {
		t.Errorf("unexpected request: %+v", got)
	}
	a.Resolve(got.ID, DecisionOnce)
	if a.resolved["a1"] != DecisionOnce {
		t.Errorf("resolved[%q] = %v, want DecisionOnce", "a1", a.resolved["a1"])
	}
}

func TestDecisionValuesAreDistinct(t *testing.T) {
	seen := map[Decision]bool{}
	for _, d := range []Decision{DecisionOnce, DecisionAlways, DecisionDeny, DecisionDenyAlways} {
		if seen[d] {
			t.Fatalf("duplicate Decision value: %v", d)
		}
		seen[d] = true
	}
}

type fakeSessionStore struct {
	sessions map[string]SessionMeta
}

func (s *fakeSessionStore) List() ([]SessionMeta, error) {
	out := make([]SessionMeta, 0, len(s.sessions))
	for _, m := range s.sessions {
		out = append(out, m)
	}
	return out, nil
}
func (s *fakeSessionStore) Load(name string) error {
	if _, ok := s.sessions[name]; !ok {
		return errNotFound
	}
	return nil
}
func (s *fakeSessionStore) Save(name string) error {
	if s.sessions == nil {
		s.sessions = map[string]SessionMeta{}
	}
	s.sessions[name] = SessionMeta{Name: name, UpdatedAt: time.Now()}
	return nil
}

var _ SessionStore = (*fakeSessionStore)(nil)

var errNotFound = &notFoundError{}

type notFoundError struct{}

func (*notFoundError) Error() string { return "session not found" }

func TestSessionStoreSaveListLoad(t *testing.T) {
	s := &fakeSessionStore{}
	if err := s.Save("work"); err != nil {
		t.Fatal(err)
	}
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Name != "work" {
		t.Errorf("unexpected list: %+v", list)
	}
	if err := s.Load("work"); err != nil {
		t.Errorf("Load(%q): %v", "work", err)
	}
	if err := s.Load("missing"); err == nil {
		t.Error("expected error loading missing session")
	}
}
