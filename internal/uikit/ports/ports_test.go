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

// TestWithLiveTotalNeverGrowsTheFloor is the regression for the bug this
// method replaced. The session adopts a turn's messages only at turn end, so
// mid-turn its composition is the previous turn's. Scaling that composition up
// to the provider's growing total made the system prompt and the tool schemas
// grow on screen, which is impossible, while every conversation row sat at
// zero. The floor must come out byte for byte unchanged and the unexplained
// remainder must be visible as pending.
func TestWithLiveTotalNeverGrowsTheFloor(t *testing.T) {
	// A first turn: the session has priced the floor and nothing else.
	floorOnly := ContextBreakdown{
		System: 6_000, ToolSchemas: 3_000, ExternalSchemas: 5_000, Memory: 2_000,
		ToolCount: 19, ExternalToolCount: 12,
	}
	got := floorOnly.WithLiveTotal(90_000)
	if got.System != 6_000 || got.ToolSchemas != 3_000 || got.ExternalSchemas != 5_000 || got.Memory != 2_000 {
		t.Errorf("the floor moved: system=%d tools=%d servers=%d memory=%d, want 6000/3000/5000/2000",
			got.System, got.ToolSchemas, got.ExternalSchemas, got.Memory)
	}
	if got.Pending != 74_000 {
		t.Errorf("Pending = %d, want the 74000 the composition cannot explain", got.Pending)
	}
	if got.Total() != 90_000 {
		t.Errorf("Total = %d, want the live 90000 so the rows sum to the header", got.Total())
	}
	if got.ToolCount != 19 || got.ExternalToolCount != 12 {
		t.Errorf("schema counts = %d/%d, want 19/12", got.ToolCount, got.ExternalToolCount)
	}
}

// TestWithLiveTotalKeepsAnAdoptedComposition: once a turn is adopted the
// session explains the whole total, so nothing is pending and no row moves.
func TestWithLiveTotalKeepsAnAdoptedComposition(t *testing.T) {
	full := ContextBreakdown{
		System: 6_000, ToolSchemas: 3_000, Memory: 1_000,
		Prose: 12_000, ToolResults: 36_000, Reasoning: 2_000,
	}
	got := full.WithLiveTotal(full.Total())
	if got != full {
		t.Errorf("an exact total changed the composition:\ngot  %+v\nwant %+v", got, full)
	}
	if got.Pending != 0 {
		t.Errorf("Pending = %d, want 0 when the composition explains the total", got.Pending)
	}
}

// TestWithLiveTotalShrinksOnlyTheConversation: a total below what is priced
// means compaction just dropped history. Compaction removes conversation and
// never the floor, so the floor must survive and only the conversation shrinks.
func TestWithLiveTotalShrinksOnlyTheConversation(t *testing.T) {
	before := ContextBreakdown{
		System: 6_000, ToolSchemas: 3_000, Memory: 1_000,
		Prose: 20_000, ToolResults: 60_000, Reasoning: 10_000,
	}
	got := before.WithLiveTotal(30_000)
	if got.Floor() != before.Floor() {
		t.Errorf("floor = %d after compaction, want it unchanged at %d", got.Floor(), before.Floor())
	}
	if got.Total() != 30_000 {
		t.Errorf("Total = %d, want 30000", got.Total())
	}
	if got.ToolResults <= got.Prose {
		t.Errorf("composition lost while shrinking: results=%d prose=%d", got.ToolResults, got.Prose)
	}
}

// TestWithLiveTotalOnAnEmptyComposition: with nothing priced at all the whole
// total is pending rather than invented as a split across rows that would each
// be a guess.
func TestWithLiveTotalOnAnEmptyComposition(t *testing.T) {
	got := ContextBreakdown{ToolCount: 4, ExternalToolCount: 1}.WithLiveTotal(50_000)
	if got.Pending != 50_000 || got.Total() != 50_000 {
		t.Errorf("Pending = %d, Total = %d, want both 50000", got.Pending, got.Total())
	}
	if got.System != 0 || got.Prose != 0 {
		t.Errorf("invented a composition: system=%d prose=%d", got.System, got.Prose)
	}
	if got.ToolCount != 4 || got.ExternalToolCount != 1 {
		t.Errorf("counts = %d/%d, want 4/1", got.ToolCount, got.ExternalToolCount)
	}
}

// TestWithLiveTotalBelowTheFloorStaysConsistent: a degenerate reading (a total
// smaller than the floor itself) must still leave rows that sum to it rather
// than a negative or a contradiction.
func TestWithLiveTotalBelowTheFloorStaysConsistent(t *testing.T) {
	got := ContextBreakdown{System: 6_000, ToolSchemas: 4_000, Prose: 1_000}.WithLiveTotal(2_000)
	if got.Total() != 2_000 {
		t.Errorf("Total = %d, want 2000", got.Total())
	}
	for name, v := range map[string]int64{"System": got.System, "ToolSchemas": got.ToolSchemas, "Prose": got.Prose, "Pending": got.Pending} {
		if v < 0 {
			t.Errorf("%s = %d, want no negative bucket", name, v)
		}
	}
}

// TestWithLiveTotalCarriesEverySkillBucket: the five existing
// WithLiveTotal fixtures all leave Skills at zero, so the field was
// threaded through Conversation, buckets and conversationBuckets without
// a single case exercising it - and dropping it from any of the three
// broke the sum invariant with nothing failing.
func TestWithLiveTotalCarriesEverySkillBucket(t *testing.T) {
	base := ContextBreakdown{
		System: 1_000, ToolSchemas: 500, ExternalSchemas: 250,
		Memory: 100, Summary: 50,
		Skills: 4_000, SkillCount: 2,
		Prose: 800, ToolResults: 600, Reasoning: 200,
	}
	// Above the composition, at it, between floor and it, and below the
	// floor: every branch of WithLiveTotal.
	for _, total := range []int64{20_000, base.Total(), 3_000, 1_500, 900, 1} {
		got := base.WithLiveTotal(total)
		if got.Total() != total {
			t.Errorf("WithLiveTotal(%d).Total() = %d, want exactly %d", total, got.Total(), total)
		}
		if got.SkillCount != base.SkillCount {
			t.Errorf("WithLiveTotal(%d) changed SkillCount to %d: a count is not a token cost",
				total, got.SkillCount)
		}
		if got.Skills < 0 {
			t.Errorf("WithLiveTotal(%d) drove Skills negative: %d", total, got.Skills)
		}
	}

	// A total above the composition leaves the priced buckets alone and
	// puts the remainder on Pending - Skills included, or the skill cost
	// would be double counted against the same tokens.
	got := base.WithLiveTotal(20_000)
	if got.Skills != base.Skills {
		t.Errorf("an above-composition total rescaled Skills to %d, want %d", got.Skills, base.Skills)
	}
	if got.Pending != 20_000-base.Total() {
		t.Errorf("Pending = %d, want %d", got.Pending, 20_000-base.Total())
	}

	// Between floor and composition, compaction dropped history: the
	// floor stays whole and Skills scales with the rest of the
	// conversation rather than being spared or zeroed.
	shrunk := base.WithLiveTotal(base.Floor() + 100)
	if shrunk.Floor() != base.Floor() {
		t.Errorf("a shrinking total rescaled the floor: %d, want %d", shrunk.Floor(), base.Floor())
	}
	if shrunk.Skills == 0 || shrunk.Skills >= base.Skills {
		t.Errorf("Skills = %d, want it scaled down but not erased (was %d)", shrunk.Skills, base.Skills)
	}
}

// TestScaleFieldsPutsAnUnattributableTotalOnTheLastField pins the
// degenerate arm: nothing priced, but a total to account for.
//
// It is tested directly because it is not reachable through
// WithLiveTotal today - that function only calls scaleFields with the
// whole bucket set when total <= floor, and a zero sum there implies
// floor == 0, which contradicts total > 0. The arm still has to be
// right: callers order the fields so the LAST one is the only bucket
// that can honestly carry an amount nothing explains (Pending), and
// spreading the remainder evenly instead would invent a composition the
// session never had.
func TestScaleFieldsPutsAnUnattributableTotalOnTheLastField(t *testing.T) {
	var first, middle, last int64
	scaleFields([]*int64{&first, &middle, &last}, 900)

	if first != 0 || middle != 0 {
		t.Errorf("the remainder was spread across buckets (%d, %d, %d); that invents a composition", first, middle, last)
	}
	if last != 900 {
		t.Errorf("last field = %d, want the whole 900", last)
	}

	// A previously-scaled set is zeroed first, so a stale composition
	// cannot survive underneath the new total.
	stale := []int64{5, 7, 11}
	scaleFields([]*int64{&stale[0], &stale[1], &stale[2]}, 0)
	if stale[0] != 0 || stale[1] != 0 || stale[2] != 0 {
		t.Errorf("scaling to zero left %v behind", stale)
	}
}

// TestWithLiveTotalOfNothingKeepsOnlyTheCounts: a provider that reported
// no input tokens yet - a turn that has not been priced - must not leave
// stale magnitudes on screen. The counts stay, because "19 tools" is true
// before a single token is spent.
func TestWithLiveTotalOfNothingKeepsOnlyTheCounts(t *testing.T) {
	base := ContextBreakdown{
		System: 1_000, Skills: 400, Prose: 200,
		ToolCount: 19, ExternalToolCount: 3, SkillCount: 2,
	}
	for _, total := range []int64{0, -1, -900} {
		got := base.WithLiveTotal(total)
		if got.Total() != 0 {
			t.Errorf("WithLiveTotal(%d).Total() = %d, want 0", total, got.Total())
		}
		if got.ToolCount != 19 || got.ExternalToolCount != 3 || got.SkillCount != 2 {
			t.Errorf("WithLiveTotal(%d) dropped the counts: %+v", total, got)
		}
	}
}

// TestScaleFieldsRefusesNonsense: no fields to scale, or a negative
// target, leaves everything untouched rather than writing a negative
// magnitude into a bucket that is displayed.
func TestScaleFieldsRefusesNonsense(t *testing.T) {
	scaleFields(nil, 100) // must not panic

	a, b := int64(7), int64(11)
	scaleFields([]*int64{&a, &b}, -1)
	if a != 7 || b != 11 {
		t.Errorf("a negative target rewrote the buckets to (%d, %d), want (7, 11)", a, b)
	}
}
