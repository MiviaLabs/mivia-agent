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
	// cancelable, when set, is the callID CancelToolCall reports finding.
	// Any other ID is a miss - this is enough for a fake standing in for
	// "one call is in flight".
	cancelable string
}

func (h *fakeTurnHandle) ID() string                   { return h.id }
func (h *fakeTurnHandle) Events() <-chan uievent.Event { return h.events }
func (h *fakeTurnHandle) Cancel()                      { close(h.events) }
func (h *fakeTurnHandle) CancelToolCall(callID string) bool {
	return h.cancelable != "" && callID == h.cancelable
}

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
func (c *fakeConversation) History() []Message             { return c.history }
func (c *fakeConversation) ActiveTurn() (TurnHandle, bool) { return nil, false }
func (c *fakeConversation) Model() ModelInfo               { return c.model }
func (c *fakeConversation) ContextUsage() Usage            { return c.usage }
func (c *fakeConversation) Title() string                  { return "fake title" }
func (c *fakeConversation) ID() string                     { return "fake-conv" }

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

// TestBudgetIsCappedSeparatesAConfigCapFromAModelLimit: the flag exists so a
// surface can say a small budget is a choice. Reporting true for an ordinary
// output reserve would put "capped" on every session; reporting false for a
// real cap leaves a 1M-window model looking like it lost most of its capacity,
// which is the report that prompted this field.
func TestBudgetIsCappedSeparatesAConfigCapFromAModelLimit(t *testing.T) {
	cases := []struct {
		name           string
		budget, window int64
		want           bool
	}{
		{"operator cap well below the window", 400_000, 1_048_576, true},
		{"window reduced only by an output reserve", 917_504, 1_048_576, false},
		{"budget equals the window", 200_000, 200_000, false},
		{"window undeclared", 400_000, 0, false},
		{"budget unknown", 0, 1_048_576, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info := ModelInfo{ContextWindow: tc.budget, DeclaredWindow: tc.window}
			if got := info.BudgetIsCapped(); got != tc.want {
				t.Errorf("BudgetIsCapped() = %v for budget %d of window %d, want %v",
					got, tc.budget, tc.window, tc.want)
			}
		})
	}
}

// TestContextBreakdownScaleToPreservesCompositionAndCounts: ScaleTo runs on
// every live turn, where the provider supplies the total and the session the
// composition. The parts must land on the total exactly, and the schema counts
// must not be scaled as if they were token costs.
func TestContextBreakdownScaleToPreservesCompositionAndCounts(t *testing.T) {
	raw := ContextBreakdown{
		System: 1_000, ToolSchemas: 3_000, ExternalSchemas: 5_000,
		Memory: 500, Summary: 200, Prose: 1_200, ToolResults: 9_000, Reasoning: 100,
		ToolCount: 19, ExternalToolCount: 12,
	}
	for _, total := range []int64{1, 999, 20_101, 96_000, raw.Total()} {
		got := raw.ScaleTo(total)
		if got.Total() != total {
			t.Errorf("ScaleTo(%d).Total() = %d, want exactly %d", total, got.Total(), total)
		}
		if got.ToolCount != 19 || got.ExternalToolCount != 12 {
			t.Errorf("ScaleTo(%d) changed the schema counts to %d/%d, want 19/12",
				total, got.ToolCount, got.ExternalToolCount)
		}
	}
	// Composition survives: tool results dominated before and must after.
	scaled := raw.ScaleTo(96_000)
	if scaled.ToolResults <= scaled.ExternalSchemas {
		t.Errorf("composition lost: ToolResults = %d, ExternalSchemas = %d", scaled.ToolResults, scaled.ExternalSchemas)
	}
}

// TestContextBreakdownScaleToOnAnEmptyComposition: a session with nothing
// priced must not divide by zero, and must not invent a composition it does
// not have just because a total arrived.
func TestContextBreakdownScaleToOnAnEmptyComposition(t *testing.T) {
	got := ContextBreakdown{ToolCount: 4, ExternalToolCount: 1}.ScaleTo(50_000)
	if got.Total() != 0 {
		t.Errorf("empty composition scaled to %d, want 0", got.Total())
	}
	if got.ToolCount != 4 || got.ExternalToolCount != 1 {
		t.Errorf("counts = %d/%d, want 4/1", got.ToolCount, got.ExternalToolCount)
	}
}
