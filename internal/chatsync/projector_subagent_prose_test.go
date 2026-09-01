package chatsync

import (
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/redact"
)

// proseOpts enables both prose gates so these tests exercise the published
// path. With them off the projector withholds text, which is a different
// contract covered by TestProjectorSubagentProseHonorsGates.
func proseOpts() ProjectorOptions {
	return ProjectorOptions{StreamAssistant: true, IncludeThinking: true}
}

func subagentEvent(kind events.Kind, task, content, detail string) events.Event {
	ev := events.Event{
		Kind:      kind,
		SessionID: "sess-1",
		TurnID:    "turn:1",
		Timestamp: time.Now(),
		Content:   content,
		Detail:    detail,
	}
	return ev.WithAgentAttribution(task, "builder", 1)
}

func rootEvent(kind events.Kind, content, detail string) events.Event {
	return events.Event{
		Kind:      kind,
		SessionID: "sess-1",
		TurnID:    "turn:1",
		Timestamp: time.Now(),
		Content:   content,
		Detail:    detail,
	}
}

// TestSubagentProseUsesItsOwnTypes proves a subagent's output never reaches
// the wire under a ROOT type. A viewer that predates these types keeps
// subagent output out of the main transcript on the type prefix alone, and it
// can only do that if the producer never reuses the root types.
func TestSubagentProseUsesItsOwnTypes(t *testing.T) {
	p := NewProjector("sess-1", 0, proseOpts())

	got := p.Project(subagentEvent(events.KindAssistant, "task-1", "hello", "delta"))
	if len(got) != 1 {
		t.Fatalf("subagent delta produced %d wire events, want 1", len(got))
	}
	if got[0].Type != TypeSubagentAssistantDelta {
		t.Errorf("type = %s, want %s", got[0].Type, TypeSubagentAssistantDelta)
	}

	got = p.Project(subagentEvent(events.KindThinking, "task-1", "reasoning", ""))
	if len(got) != 1 || got[0].Type != TypeSubagentThinkingDelta {
		t.Fatalf("subagent thinking type = %v, want %s", got, TypeSubagentThinkingDelta)
	}

	got = p.Project(subagentEvent(events.KindAssistant, "task-1", "hello", ""))
	if len(got) != 1 || got[0].Type != TypeSubagentAssistantMessage {
		t.Fatalf("subagent aggregate type = %v, want %s", got, TypeSubagentAssistantMessage)
	}
}

// TestSubagentDeltaDoesNotBlankRootAssistantMessage is the regression this
// whole routing branch exists for.
//
// Before the branch, a subagent's delta folded into the ROOT turn's state: it
// set streamed and incremented fragments there. The root's own aggregate then
// took the "already streamed" path and shipped EMPTY text with a non-zero
// fragment count - and a viewer renders text only when fragments is zero, so
// the root's message came out blank in a turn where the root loop never
// streamed a single token itself.
func TestSubagentDeltaDoesNotBlankRootAssistantMessage(t *testing.T) {
	p := NewProjector("sess-1", 0, proseOpts())

	// A subagent streams while the root loop does not stream at all.
	p.Project(subagentEvent(events.KindAssistant, "task-1", "sub chunk", "delta"))

	got := p.Project(rootEvent(events.KindAssistant, "root answer", ""))
	if len(got) != 1 {
		t.Fatalf("root aggregate produced %d wire events, want 1", len(got))
	}
	payload, ok := got[0].Payload.(*AssistantMessagePayload)
	if !ok {
		t.Fatalf("root aggregate payload is %T, want *AssistantMessagePayload", got[0].Payload)
	}
	if payload.Fragments != 0 {
		t.Errorf("root fragments = %d, want 0 - the root loop streamed nothing", payload.Fragments)
	}
	if payload.Text != "root answer" {
		t.Errorf("root text = %q, want %q", payload.Text, "root answer")
	}
}

// TestSubagentLanesKeepSeparateFragmentIndexes proves two runs streaming at
// once do not share a counter. A shared counter would make one run's indexes
// skip and the other's start mid-sequence, and a viewer that orders fragments
// by index would interleave two agents' sentences.
func TestSubagentLanesKeepSeparateFragmentIndexes(t *testing.T) {
	p := NewProjector("sess-1", 0, proseOpts())

	indexOf := func(we WireEvent) int {
		t.Helper()
		payload, ok := we.Payload.(*SubagentAssistantDeltaPayload)
		if !ok {
			t.Fatalf("payload is %T, want *SubagentAssistantDeltaPayload", we.Payload)
		}
		return payload.Index
	}

	a1 := p.Project(subagentEvent(events.KindAssistant, "task-a", "a1", "delta"))
	b1 := p.Project(subagentEvent(events.KindAssistant, "task-b", "b1", "delta"))
	a2 := p.Project(subagentEvent(events.KindAssistant, "task-a", "a2", "delta"))

	if got := indexOf(a1[0]); got != 0 {
		t.Errorf("task-a first index = %d, want 0", got)
	}
	if got := indexOf(b1[0]); got != 0 {
		t.Errorf("task-b first index = %d, want 0 - each run counts its own fragments", got)
	}
	if got := indexOf(a2[0]); got != 1 {
		t.Errorf("task-a second index = %d, want 1", got)
	}
}

// TestSubagentAggregateHoldsINV1PerLane proves INV-1 (text empty exactly when
// fragments is non-zero) is evaluated per run, not per turn.
func TestSubagentAggregateHoldsINV1PerLane(t *testing.T) {
	p := NewProjector("sess-1", 0, proseOpts())

	// task-a streams; task-b does not.
	p.Project(subagentEvent(events.KindAssistant, "task-a", "chunk", "delta"))

	aggA := p.Project(subagentEvent(events.KindAssistant, "task-a", "full a", ""))
	payloadA := aggA[0].Payload.(*SubagentAssistantMessagePayload)
	if payloadA.Fragments != 1 || payloadA.Text != "" {
		t.Errorf("streamed run: fragments = %d text = %q, want 1 and empty",
			payloadA.Fragments, payloadA.Text)
	}

	aggB := p.Project(subagentEvent(events.KindAssistant, "task-b", "full b", ""))
	payloadB := aggB[0].Payload.(*SubagentAssistantMessagePayload)
	if payloadB.Fragments != 0 || payloadB.Text != "full b" {
		t.Errorf("unstreamed run: fragments = %d text = %q, want 0 and %q",
			payloadB.Fragments, payloadB.Text, "full b")
	}
}

// TestSubagentProseBlockIsLaneScoped proves each run's prose gets its own
// block key. Sharing the root's "turn:assistant" key would merge every
// agent's text into one block in any viewer that groups on it.
func TestSubagentProseBlockIsLaneScoped(t *testing.T) {
	p := NewProjector("sess-1", 0, proseOpts())

	a := p.Project(subagentEvent(events.KindAssistant, "task-a", "a", "delta"))
	b := p.Project(subagentEvent(events.KindAssistant, "task-b", "b", "delta"))
	root := p.Project(rootEvent(events.KindAssistant, "r", "delta"))

	blockA := a[0].Payload.(*SubagentAssistantDeltaPayload).Block
	blockB := b[0].Payload.(*SubagentAssistantDeltaPayload).Block
	blockRoot := root[0].Payload.(*AssistantDeltaPayload).Block

	if blockA == blockB {
		t.Errorf("two runs share block %q; each run needs its own", blockA)
	}
	if blockA == blockRoot || blockB == blockRoot {
		t.Errorf("a subagent shares the root's block %q", blockRoot)
	}
}

// TestProjectorSubagentProseHonorsGates proves subagent prose is held to the
// SAME controls as the root loop's text - there is no separate subagent knob,
// so an operator who withholds root prose withholds a subagent's too.
func TestProjectorSubagentProseHonorsGates(t *testing.T) {
	p := NewProjector("sess-1", 0, ProjectorOptions{StreamAssistant: false, IncludeThinking: false})

	if got := p.Project(subagentEvent(events.KindAssistant, "task-1", "hidden", "delta")); len(got) != 0 {
		t.Errorf("delta produced %d wire events with StreamAssistant off, want 0", len(got))
	}

	got := p.Project(subagentEvent(events.KindThinking, "task-1", "secret reasoning", ""))
	if len(got) != 1 {
		t.Fatalf("thinking produced %d wire events, want 1", len(got))
	}
	payload := got[0].Payload.(*SubagentThinkingDeltaPayload)
	if payload.Text != "" {
		t.Errorf("thinking text = %q, want empty with IncludeThinking off", payload.Text)
	}
	if payload.Bytes != len("secret reasoning") {
		t.Errorf("thinking bytes = %d, want the real size so a viewer can show activity", payload.Bytes)
	}
}

// TestSubagentLaneStateIsBounded proves a wide fan-out cannot grow the lane
// map without limit, and - the reason the lanes live in their own map - that
// it cannot evict the ROOT turn's state either. A root turn whose state was
// evicted mid-turn re-streams its aggregate wrongly.
func TestSubagentLaneStateIsBounded(t *testing.T) {
	p := NewProjector("sess-1", 0, proseOpts())

	// Establish root turn state by streaming one root delta.
	p.Project(rootEvent(events.KindAssistant, "root chunk", "delta"))

	for i := range maxTrackedLanes * 3 {
		task := "task-" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		p.Project(subagentEvent(events.KindAssistant, task, "x", "delta"))
	}

	if len(p.lanes) > maxTrackedLanes {
		t.Errorf("lane map holds %d entries, want at most %d", len(p.lanes), maxTrackedLanes)
	}
	if len(p.laneOrder) != len(p.lanes) {
		t.Errorf("lane order holds %d keys but map holds %d", len(p.laneOrder), len(p.lanes))
	}

	// The root's own state survived the fan-out: its aggregate still reports
	// the single fragment it actually streamed.
	got := p.Project(rootEvent(events.KindAssistant, "root answer", ""))
	payload := got[0].Payload.(*AssistantMessagePayload)
	if payload.Fragments != 1 {
		t.Errorf("root fragments = %d, want 1 - lane churn evicted the root turn's state",
			payload.Fragments)
	}
}

// TestSubagentProseIsRedacted is the safety proof for the whole change. The
// reason publishing a subagent's prose is defensible is that it passes through
// the same redaction as the root loop's text. Nothing enforced that claim, so
// the redaction call could be deleted from either subagent path and every test
// stayed green while secrets shipped.
func TestSubagentProseIsRedacted(t *testing.T) {
	pol, err := redact.Compile([]string{`SECRET_KEY_[0-9]+`}, nil, "[redacted]")
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	oldPol := redact.Current()
	redact.SetPolicy(pol)
	t.Cleanup(func() { redact.SetPolicy(oldPol) })

	p := NewProjector("sess-1", 0, proseOpts())
	const secret = "token is SECRET_KEY_998877 do not leak"

	delta := p.Project(subagentEvent(events.KindAssistant, "task-1", secret, "delta"))
	deltaText := delta[0].Payload.(*SubagentAssistantDeltaPayload).Text
	assertRedacted(t, "assistant delta", deltaText)

	agg := p.Project(subagentEvent(events.KindAssistant, "task-2", secret, ""))
	aggText := agg[0].Payload.(*SubagentAssistantMessagePayload).Text
	assertRedacted(t, "assistant message", aggText)

	think := p.Project(subagentEvent(events.KindThinking, "task-1", secret, ""))
	thinkText := think[0].Payload.(*SubagentThinkingDeltaPayload).Text
	assertRedacted(t, "thinking delta", thinkText)
}

func assertRedacted(t *testing.T, label, got string) {
	t.Helper()
	if strings.Contains(got, "SECRET_KEY_998877") {
		t.Errorf("%s = %q, carries an unredacted secret", label, got)
	}
	if !strings.Contains(got, "[redacted]") {
		t.Errorf("%s = %q, missing the redaction placeholder", label, got)
	}
}

// TestSubagentProseIsTruncated proves the byte budgets apply to a subagent's
// text as well. An unbounded field is how one runaway subagent exhausts the
// 64 KiB per-event ceiling the API enforces and poisons the stream.
func TestSubagentProseIsTruncated(t *testing.T) {
	p := NewProjector("sess-1", 0, proseOpts())
	huge := strings.Repeat("x", BudgetAssistantText+BudgetDeltaText)

	delta := p.Project(subagentEvent(events.KindAssistant, "task-1", huge, "delta"))
	deltaPayload := delta[0].Payload.(*SubagentAssistantDeltaPayload)
	if len(deltaPayload.Text) > BudgetDeltaText {
		t.Errorf("delta text is %d bytes, over the %d budget", len(deltaPayload.Text), BudgetDeltaText)
	}
	if deltaPayload.Trunc == nil || len(deltaPayload.Trunc.Fields) == 0 {
		t.Error("delta was truncated but recorded no trunc.fields entry")
	}

	agg := p.Project(subagentEvent(events.KindAssistant, "task-2", huge, ""))
	aggPayload := agg[0].Payload.(*SubagentAssistantMessagePayload)
	if len(aggPayload.Text) > BudgetAssistantText {
		t.Errorf("aggregate text is %d bytes, over the %d budget", len(aggPayload.Text), BudgetAssistantText)
	}
	if aggPayload.Bytes != len(huge) {
		t.Errorf("aggregate bytes = %d, want the real size %d", aggPayload.Bytes, len(huge))
	}

	think := p.Project(subagentEvent(events.KindThinking, "task-1", huge, ""))
	thinkPayload := think[0].Payload.(*SubagentThinkingDeltaPayload)
	if len(thinkPayload.Text) > BudgetDeltaText {
		t.Errorf("thinking text is %d bytes, over the %d budget", len(thinkPayload.Text), BudgetDeltaText)
	}
}

// TestSubagentThinkingIsLaneScoped pins the thinking path the way the
// assistant path is pinned. Thinking has its own counter and its own block
// key, and reverting either to the root's - the exact bug this change fixes
// for assistant text - previously passed every test.
func TestSubagentThinkingIsLaneScoped(t *testing.T) {
	p := NewProjector("sess-1", 0, proseOpts())

	// The root streams thinking first, so a shared counter would be visible.
	p.Project(rootEvent(events.KindThinking, "root reasoning", ""))

	a := p.Project(subagentEvent(events.KindThinking, "task-a", "a", ""))
	b := p.Project(subagentEvent(events.KindThinking, "task-b", "b", ""))
	a2 := p.Project(subagentEvent(events.KindThinking, "task-a", "a again", ""))

	payloadA := a[0].Payload.(*SubagentThinkingDeltaPayload)
	payloadB := b[0].Payload.(*SubagentThinkingDeltaPayload)
	payloadA2 := a2[0].Payload.(*SubagentThinkingDeltaPayload)

	if payloadA.Index != 0 || payloadB.Index != 0 {
		t.Errorf("first thinking indexes = %d and %d, want 0 each - one counter per run",
			payloadA.Index, payloadB.Index)
	}
	if payloadA2.Index != 1 {
		t.Errorf("task-a second thinking index = %d, want 1", payloadA2.Index)
	}
	if payloadA.Block == payloadB.Block {
		t.Errorf("two runs share thinking block %q", payloadA.Block)
	}
	rootBlock := p.Project(rootEvent(events.KindThinking, "more root", ""))[0].
		Payload.(*ThinkingDeltaPayload).Block
	if payloadA.Block == rootBlock {
		t.Errorf("a subagent shares the root's thinking block %q", rootBlock)
	}
}

// TestRetiredLaneCannotBeEvictedIntoDuplicateText proves a finished run frees
// its slot. Without retirement, finished runs crowd the bounded map until they
// age out, which is what lets a LIVE run lose its state mid-stream - and a run
// that loses ls.streamed re-ships as full text the answer a viewer already
// received delta by delta.
func TestRetiredLaneCannotBeEvictedIntoDuplicateText(t *testing.T) {
	p := NewProjector("sess-1", 0, proseOpts())

	done := subagentEvent(events.KindSubagentDone, "task-done", "", "completed")
	p.Project(subagentEvent(events.KindAssistant, "task-done", "chunk", "delta"))
	if len(p.lanes) != 1 {
		t.Fatalf("lane count = %d before the terminal event, want 1", len(p.lanes))
	}

	p.Project(done)

	if len(p.lanes) != 0 {
		t.Errorf("lane count = %d after the run finished, want 0", len(p.lanes))
	}
	if len(p.laneOrder) != 0 {
		t.Errorf("lane order holds %d keys after the run finished, want 0", len(p.laneOrder))
	}
}

// TestSubagentBeginProjectsRunStart proves the run-level opening signal
// reaches the wire with the task it was given. Before it existed a consumer
// could only infer a run from its first nested tool call, and the task text
// was reachable only by correlating the dispatching tool call separately.
func TestSubagentBeginProjectsRunStart(t *testing.T) {
	p := NewProjector("sess-1", 0, proseOpts())

	ev := subagentEvent(events.KindSubagentBegin, "task-1", "", "review the diff")
	ev.Name = "reviewer"
	got := p.Project(ev)

	if len(got) != 1 {
		t.Fatalf("begin produced %d wire events, want 1", len(got))
	}
	if got[0].Type != TypeSubagentStarted {
		t.Fatalf("type = %s, want %s", got[0].Type, TypeSubagentStarted)
	}
	payload := got[0].Payload.(*SubagentStartedPayload)
	if payload.Task != "review the diff" {
		t.Errorf("task = %q, want the task description", payload.Task)
	}
	if payload.Name != "reviewer" {
		t.Errorf("name = %q, want reviewer", payload.Name)
	}
	if payload.At.IsZero() {
		t.Error("envelope carries no timestamp; it is the run's start time")
	}
}

// TestSubagentBeginPromptIsRedacted closes the same hole for the task text a
// run is given, which is user-authored and can carry anything.
func TestSubagentBeginPromptIsRedacted(t *testing.T) {
	pol, err := redact.Compile([]string{`SECRET_KEY_[0-9]+`}, nil, "[redacted]")
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	oldPol := redact.Current()
	redact.SetPolicy(pol)
	t.Cleanup(func() { redact.SetPolicy(oldPol) })

	p := NewProjector("sess-1", 0, proseOpts())
	got := p.Project(subagentEvent(events.KindSubagentBegin, "task-1", "", "use SECRET_KEY_112233 to log in"))
	assertRedacted(t, "begin task", got[0].Payload.(*SubagentStartedPayload).Task)
}

// TestEnvelopeCarriesParentTask proves a nested run reports WHICH run started
// it. Depth alone cannot rebuild the tree: two runs at depth 2 under different
// parents are indistinguishable by depth, so a viewer had to render every run
// as a sibling of every other.
func TestEnvelopeCarriesParentTask(t *testing.T) {
	p := NewProjector("sess-1", 0, proseOpts())

	child := subagentEvent(events.KindSubagentBegin, "task-child", "", "nested work")
	child = child.WithAgentParent("task-parent")
	got := p.Project(child)

	payload := got[0].Payload.(*SubagentStartedPayload)
	if payload.Agent == nil {
		t.Fatal("envelope carries no agent origin")
	}
	if payload.Agent.ParentTask != "task-parent" {
		t.Errorf("parent_task = %q, want task-parent", payload.Agent.ParentTask)
	}

	// A run the root dispatched reports no parent at all, so a consumer can
	// read an absent parent as "top level" rather than "unknown".
	top := p.Project(subagentEvent(events.KindSubagentBegin, "task-top", "", "top work"))
	if parent := top[0].Payload.(*SubagentStartedPayload).Agent.ParentTask; parent != "" {
		t.Errorf("root-dispatched run reports parent %q, want empty", parent)
	}
}
