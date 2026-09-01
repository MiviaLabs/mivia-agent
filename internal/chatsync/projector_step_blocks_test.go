package chatsync

import (
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// A turn is a LOOP: the model talks, calls a tool, reads the result, talks
// again. Every fragment of that loop used to carry one block id per turn, so a
// consumer keying on the block - which is the only key the wire offers - had no
// way to tell one utterance from the next. It welded a whole turn's narration
// into a single message and lost the order it interleaved with the tool calls.
//
// These tests pin the segment boundary at the producer, where the step
// structure is actually known.

func stepEvent(kind events.Kind, content, detail string) events.Event {
	return events.Event{
		Kind:      kind,
		SessionID: "sess-1",
		TurnID:    "turn:1",
		Timestamp: time.Now(),
		Content:   content,
		Detail:    detail,
	}
}

func toolEvent(kind events.Kind, callID string) events.Event {
	return events.Event{
		Kind:       kind,
		SessionID:  "sess-1",
		TurnID:     "turn:1",
		Timestamp:  time.Now(),
		ToolCallID: callID,
		Name:       "Read",
	}
}

// blockOf reads the envelope block off whichever prose payload the projector
// produced. A type switch rather than an interface assertion: the payloads are
// distinct structs that happen to embed the same Envelope.
func blockOf(t *testing.T, we WireEvent) string {
	t.Helper()
	switch p := we.Payload.(type) {
	case *AssistantDeltaPayload:
		return p.Block
	case *AssistantMessagePayload:
		return p.Block
	case *ThinkingDeltaPayload:
		return p.Block
	case *AssistantResetPayload:
		return p.Block
	case *SubagentAssistantDeltaPayload:
		return p.Block
	case *SubagentThinkingDeltaPayload:
		return p.Block
	default:
		t.Fatalf("payload %T carries no prose block", we.Payload)
		return ""
	}
}

func onlyEvent(t *testing.T, got []WireEvent) WireEvent {
	t.Helper()
	if len(got) != 1 {
		t.Fatalf("projected %d wire events, want 1", len(got))
	}
	return got[0]
}

// TestProseBlockAdvancesPastEachToolCall is the whole point: the narration
// before a tool call and the narration after it must not share a block id.
func TestProseBlockAdvancesPastEachToolCall(t *testing.T) {
	p := NewProjector("sess-1", 0, proseOpts())

	first := blockOf(t, onlyEvent(t, p.Project(stepEvent(events.KindAssistant, "Let me look.", "delta"))))
	p.Project(toolEvent(events.KindToolStart, "call-1"))
	p.Project(toolEvent(events.KindToolEnd, "call-1"))
	second := blockOf(t, onlyEvent(t, p.Project(stepEvent(events.KindAssistant, "Found it.", "delta"))))

	if first == second {
		t.Fatalf("prose either side of a tool call shares block %q; the two utterances are indistinguishable on the wire", first)
	}
	if first != "turn:1:assistant:0" {
		t.Errorf("first block = %q, want turn:1:assistant:0", first)
	}
	if second != "turn:1:assistant:1" {
		t.Errorf("second block = %q, want turn:1:assistant:1", second)
	}
}

// Thinking advances on the same boundary and with the same counter: a step is
// one unit of reasoning plus one unit of narration, and a consumer that renders
// them as a pair needs the two to agree on which step they belong to.
func TestThinkingSharesTheStepCounterWithAssistant(t *testing.T) {
	p := NewProjector("sess-1", 0, proseOpts())

	if got := blockOf(t, onlyEvent(t, p.Project(stepEvent(events.KindThinking, "read it first", "")))); got != "turn:1:thinking:0" {
		t.Errorf("step 0 thinking block = %q, want turn:1:thinking:0", got)
	}
	p.Project(stepEvent(events.KindAssistant, "Let me look.", "delta"))
	p.Project(toolEvent(events.KindToolStart, "call-1"))

	if got := blockOf(t, onlyEvent(t, p.Project(stepEvent(events.KindThinking, "now patch it", "")))); got != "turn:1:thinking:1" {
		t.Errorf("step 1 thinking block = %q, want turn:1:thinking:1", got)
	}
	if got := blockOf(t, onlyEvent(t, p.Project(stepEvent(events.KindAssistant, "Found it.", "delta")))); got != "turn:1:assistant:1" {
		t.Errorf("step 1 assistant block = %q, want turn:1:assistant:1", got)
	}
}

// A step that calls three tools at once is still ONE step. Bumping per tool
// would leave the next utterance in segment 3 with 1 and 2 never used - not
// wrong, but it invites a consumer to render two blank messages for the gap.
func TestParallelToolsInOneStepAdvanceTheBlockOnce(t *testing.T) {
	p := NewProjector("sess-1", 0, proseOpts())

	p.Project(stepEvent(events.KindAssistant, "Checking all three.", "delta"))
	p.Project(toolEvent(events.KindToolStart, "call-1"))
	p.Project(toolEvent(events.KindToolStart, "call-2"))
	p.Project(toolEvent(events.KindToolStart, "call-3"))
	p.Project(toolEvent(events.KindToolEnd, "call-1"))

	if got := blockOf(t, onlyEvent(t, p.Project(stepEvent(events.KindAssistant, "All read.", "delta")))); got != "turn:1:assistant:1" {
		t.Errorf("block after a parallel step = %q, want turn:1:assistant:1", got)
	}
}

// A tool call that follows no narration closes nothing. Advancing there would
// spend a segment on silence and split one utterance that a tool call merely
// interrupted without the model saying anything either side of it.
func TestToolCallWithoutPrecedingProseDoesNotAdvanceTheBlock(t *testing.T) {
	p := NewProjector("sess-1", 0, proseOpts())

	p.Project(toolEvent(events.KindToolStart, "call-1"))
	p.Project(toolEvent(events.KindToolEnd, "call-1"))

	if got := blockOf(t, onlyEvent(t, p.Project(stepEvent(events.KindAssistant, "Here.", "delta")))); got != "turn:1:assistant:0" {
		t.Errorf("block = %q, want turn:1:assistant:0 - no prose preceded the tool call", got)
	}
}

// The settled aggregate closes the segment its own deltas streamed into. Any
// other id and the message that completes a block names a block that never
// existed.
func TestSettledMessageCarriesItsSegmentsBlock(t *testing.T) {
	p := NewProjector("sess-1", 0, proseOpts())

	p.Project(stepEvent(events.KindAssistant, "Let me look.", "delta"))
	p.Project(toolEvent(events.KindToolStart, "call-1"))
	p.Project(stepEvent(events.KindAssistant, "Fixed.", "delta"))

	if got := blockOf(t, onlyEvent(t, p.Project(stepEvent(events.KindAssistant, "Let me look.Fixed.", "")))); got != "turn:1:assistant:1" {
		t.Errorf("settled message block = %q, want turn:1:assistant:1", got)
	}
}

// The reset names the STREAM, not a segment - it discards the whole turn's
// assistant text, which by now may span several segments, and one segment id
// cannot name them all. Its block id is deliberately the prefix every segment
// of that stream extends.
func TestResetNamesTheStreamNotASegment(t *testing.T) {
	p := NewProjector("sess-1", 0, proseOpts())

	p.Project(stepEvent(events.KindAssistant, "First try.", "delta"))
	p.Project(toolEvent(events.KindToolStart, "call-1"))
	p.Project(stepEvent(events.KindAssistant, "Still going.", "delta"))

	if got := blockOf(t, onlyEvent(t, p.Project(stepEvent(events.KindAssistantReset, "", "retrying")))); got != "turn:1:assistant" {
		t.Errorf("reset block = %q, want turn:1:assistant", got)
	}
}

// The replay after a reset must not land on a block id the abandoned attempt
// already used. A consumer keyed on the id alone would append the replay to
// text it was just told to discard - the exact defect the reset exists to
// prevent.
func TestReplayAfterResetUsesAFreshBlock(t *testing.T) {
	p := NewProjector("sess-1", 0, proseOpts())

	used := blockOf(t, onlyEvent(t, p.Project(stepEvent(events.KindAssistant, "First try.", "delta"))))
	p.Project(stepEvent(events.KindAssistantReset, "", "retrying"))
	replay := blockOf(t, onlyEvent(t, p.Project(stepEvent(events.KindAssistant, "Replayed answer.", "delta"))))

	if replay == used {
		t.Fatalf("replay reused the discarded attempt's block %q", used)
	}
}

// Each subagent run segments on its OWN tool calls. Sharing the root's counter
// would make two runs streaming at once jump each other's segments.
func TestSubagentLaneSegmentsOnItsOwnToolCalls(t *testing.T) {
	p := NewProjector("sess-1", 0, proseOpts())

	laneTool := func(kind events.Kind, task, callID string) events.Event {
		ev := events.Event{
			Kind:       kind,
			SessionID:  "sess-1",
			TurnID:     "turn:1",
			Timestamp:  time.Now(),
			ToolCallID: callID,
			Name:       "Read",
		}
		return ev.WithAgentAttribution(task, "builder", 1)
	}

	first := blockOf(t, onlyEvent(t, p.Project(subagentEvent(events.KindAssistant, "task-a", "Looking.", "delta"))))
	p.Project(laneTool(events.KindSubagentStart, "task-a", "call-1"))
	// A different run's tool call must not advance task-a's counter.
	p.Project(laneTool(events.KindSubagentStart, "task-b", "call-9"))
	second := blockOf(t, onlyEvent(t, p.Project(subagentEvent(events.KindAssistant, "task-a", "Done.", "delta"))))

	if first != "turn:1:task-a:assistant:0" {
		t.Errorf("first lane block = %q, want turn:1:task-a:assistant:0", first)
	}
	if second != "turn:1:task-a:assistant:1" {
		t.Errorf("second lane block = %q, want turn:1:task-a:assistant:1", second)
	}
}

// Two turns are two conversations. A segment counter that survived a turn would
// have turn 2 opening at whatever depth turn 1 happened to reach.
func TestSegmentCounterIsPerTurn(t *testing.T) {
	p := NewProjector("sess-1", 0, proseOpts())

	p.Project(stepEvent(events.KindAssistant, "Turn one.", "delta"))
	p.Project(toolEvent(events.KindToolStart, "call-1"))
	p.Project(stepEvent(events.KindAssistant, "Still turn one.", "delta"))

	next := events.Event{
		Kind:      events.KindAssistant,
		SessionID: "sess-1",
		TurnID:    "turn:2",
		Timestamp: time.Now(),
		Content:   "Turn two.",
		Detail:    "delta",
	}
	if got := blockOf(t, onlyEvent(t, p.Project(next))); got != "turn:2:assistant:0" {
		t.Errorf("new turn's first block = %q, want turn:2:assistant:0", got)
	}
}

// The settled aggregate must name the segment its own deltas streamed into.
//
// env.Block was computed once at function entry from the CURRENT segment, but
// the terminal EventAssistant is published from finalizeSDKTurn - after the
// turn's last tool call, which has already advanced the counter. The aggregate
// therefore named a segment holding nothing: an empty settled message claiming
// a fragment count, while the block holding the real text never completed.
//
// TestSettledMessageCarriesItsSegmentsBlock cannot catch this: it puts prose
// AFTER the last tool call, the one arrangement where the drift is zero.
func TestSettledMessageNamesItsDeltasSegmentAcrossAToolBoundary(t *testing.T) {
	p := NewProjector("sess-1", 0, proseOpts())

	delta := blockOf(t, onlyEvent(t, p.Project(stepEvent(events.KindAssistant, "the answer", "delta"))))
	p.Project(toolEvent(events.KindToolStart, "call-1"))
	p.Project(toolEvent(events.KindToolEnd, "call-1"))
	settled := blockOf(t, onlyEvent(t, p.Project(stepEvent(events.KindAssistant, "the answer", ""))))

	if settled != delta {
		t.Fatalf("settled message named %q, but its deltas streamed into %q - the named block holds nothing", settled, delta)
	}
}

// A LANE's reset names its lane stream, with no step suffix - the same rule the
// root path follows. The root assertion cannot carry this: `stepEvent` never
// sets AgentTask, so it only ever drives the else branch. A mutation naming the
// segment here passed the entire package.
func TestLaneResetNamesTheLaneStreamNotASegment(t *testing.T) {
	p := NewProjector("sess-1", 0, proseOpts())

	p.Project(subagentEvent(events.KindAssistant, "task-a", "First try.", "delta"))
	got := blockOf(t, onlyEvent(t, p.Project(subagentEvent(events.KindAssistantReset, "task-a", "", "retrying"))))

	if got != "turn:1:task-a:assistant" {
		t.Errorf("lane reset block = %q, want turn:1:task-a:assistant", got)
	}
}

// A lane's THINKING segments on that lane's own tool calls, not the root turn's.
// Nothing read a SubagentThinkingDeltaPayload's block before this, so a mutation
// pointing it at the root counter passed the whole package - while two
// concurrent runs would have jumped each other's reasoning ids.
func TestLaneThinkingSegmentsOnItsOwnCounter(t *testing.T) {
	p := NewProjector("sess-1", 0, proseOpts())

	first := blockOf(t, onlyEvent(t, p.Project(subagentEvent(events.KindThinking, "task-a", "planning", ""))))

	// A ROOT tool call must not move the lane's counter.
	p.Project(stepEvent(events.KindAssistant, "root talks", "delta"))
	p.Project(toolEvent(events.KindToolStart, "root-call"))

	if got := blockOf(t, onlyEvent(t, p.Project(subagentEvent(events.KindThinking, "task-a", "still planning", "")))); got != first {
		t.Errorf("lane thinking block moved to %q on a ROOT tool call; want it to stay at %q", got, first)
	}

	laneTool := events.Event{
		Kind: events.KindSubagentStart, SessionID: "sess-1", TurnID: "turn:1",
		Timestamp: time.Now(), ToolCallID: "lane-call", Name: "Read",
	}
	p.Project(laneTool.WithAgentAttribution("task-a", "builder", 1))

	if got := blockOf(t, onlyEvent(t, p.Project(subagentEvent(events.KindThinking, "task-a", "done", "")))); got != "turn:1:task-a:thinking:1" {
		t.Errorf("lane thinking block after the lane's OWN tool = %q, want turn:1:task-a:thinking:1", got)
	}
}
