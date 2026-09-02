package chatsync

import (
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// A wire event can be projected and then never stored: the outbox overflows on
// a slow or offline uplink, and appendLocked hands what it dropped back to
// RollbackStreaming. The producer's counters must then describe what the
// VIEWER holds, not what this side intended to send.
//
// These tests drive that path. The previous rollback for a reset wrote back
// exactly the values the reset had set - streamed=false, fragments=0 - which
// is not a rollback but a restatement, and nothing failed because nothing
// tested it.

// settledMessage runs the turn's aggregate and returns it.
func settledMessage(t *testing.T, p *Projector) *AssistantMessagePayload {
	t.Helper()
	got := p.Project(rootEvent(events.KindAssistant, "the second answer", ""))
	if len(got) != 1 {
		t.Fatalf("the settled aggregate produced %d wire events, want 1", len(got))
	}
	payload, ok := got[0].Payload.(*AssistantMessagePayload)
	if !ok {
		t.Fatalf("settled payload is %T, want *AssistantMessagePayload", got[0].Payload)
	}
	return payload
}

// TestALostResetMakesTheSettledMessageCarryTheAnswer is the defect.
//
// The viewer holds the abandoned attempt's fragments and never heard the
// discard. This side cannot say how many fragments that is - the reset zeroed
// the count before the append failed - so the only repair it can offer is the
// full text, which a viewer writes over its stitched text. Reporting a count
// that covers the retry alone makes INV-1 empty the text, and the viewer keeps
// two attempts welded together with nothing to replace them.
func TestALostResetMakesTheSettledMessageCarryTheAnswer(t *testing.T) {
	p := NewProjector("sess-1", 0, proseOpts())

	p.Project(rootEvent(events.KindAssistant, "the first ", "delta"))
	p.Project(rootEvent(events.KindAssistant, "answer", "delta"))

	reset := p.Project(rootEvent(events.KindAssistantReset, "", "schema_retry"))
	if len(reset) != 1 {
		t.Fatalf("the reset produced %d wire events, want 1", len(reset))
	}
	// The append lost it: it was projected, and never stored.
	p.RollbackStreaming(reset)

	// The retry streams its own answer, which is the case the previous
	// rollback got wrong - it reported a count covering only this attempt.
	p.Project(rootEvent(events.KindAssistant, "the second answer", "delta"))

	payload := settledMessage(t, p)
	if payload.Text != "the second answer" {
		t.Errorf("Text = %q, want the whole answer: the viewer still holds the "+
			"abandoned attempt and this message is the only thing that can "+
			"replace it", payload.Text)
	}
	if payload.Fragments != 0 {
		t.Errorf("Fragments = %d, want 0 - a non-zero count empties the text "+
			"under INV-1 and counts only the attempt the viewer did not lose",
			payload.Fragments)
	}
}

// TestProjector_MaxStepsExhaustion_ResetFollowsSettledAssistantMessage is the
// chatsync leg of the fix in internal/agent/loop_dispatch.go: when the SDK
// root loop's turn stops on StopMaxIterations, finalizeSDKTurn has already
// published the turn's last assistant text to the wire as a settled
// "completed" message before runOnceSDK discards it and returns a hard
// error. runOnceSDK now emits an assistant.reset for the same stream right
// after that settled message, so a viewer retracts the bubble instead of
// showing an answer the turn is about to report as a hard failure with no
// accepted reply. This proves the projector - already correct for the
// prompt-too-long and empty-response reset cases - behaves identically for
// this new call site.
func TestProjector_MaxStepsExhaustion_ResetFollowsSettledAssistantMessage(t *testing.T) {
	p := NewProjector("sess-1", 0, proseOpts())

	msg := p.Project(rootEvent(events.KindAssistant, "let me check that...", ""))
	reset := p.Project(rootEvent(events.KindAssistantReset, "",
		"agent exceeded max_steps: the reply was never accepted"))

	got := append(msg, reset...)
	wantTypes := []string{TypeAssistantMessage, TypeAssistantReset}
	if len(got) != len(wantTypes) {
		t.Fatalf("wire sequence = %v, want %v", got, wantTypes)
	}
	for i, w := range wantTypes {
		if got[i].Type != w {
			t.Fatalf("event[%d].Type = %s, want %s", i, got[i].Type, w)
		}
	}

	msgPayload, ok := got[0].Payload.(*AssistantMessagePayload)
	if !ok {
		t.Fatalf("message payload is %T, want *AssistantMessagePayload", got[0].Payload)
	}
	resetPayload, ok := got[1].Payload.(*AssistantResetPayload)
	if !ok {
		t.Fatalf("reset payload is %T, want *AssistantResetPayload", got[1].Payload)
	}
	// The reset names the STREAM (no segment suffix); the message's own
	// block extends that stream with a segment id.
	if !strings.HasPrefix(msgPayload.Block, resetPayload.Block) {
		t.Errorf("reset block = %q is not a prefix of message block = %q; the reset must target "+
			"the same stream the settled message was published on, or a viewer clears the wrong "+
			"stream's segments", resetPayload.Block, msgPayload.Block)
	}
}

// TestAStoredResetStillReportsFragments is the other half. Nothing is broken
// when the reset DID reach the wire, so the ordinary streamed accounting must
// survive: a viewer that got the discard has already cleared the attempt, and
// re-sending the whole answer beside its own deltas is the duplicate the
// fragment count exists to prevent.
func TestAStoredResetStillReportsFragments(t *testing.T) {
	p := NewProjector("sess-1", 0, proseOpts())

	p.Project(rootEvent(events.KindAssistant, "the first answer", "delta"))
	p.Project(rootEvent(events.KindAssistantReset, "", "schema_retry"))
	p.Project(rootEvent(events.KindAssistant, "the second answer", "delta"))

	payload := settledMessage(t, p)
	if payload.Fragments != 1 || payload.Text != "" {
		t.Errorf("Fragments = %d Text = %q, want 1 and empty: the viewer got the "+
			"discard and holds exactly this attempt's one delta",
			payload.Fragments, payload.Text)
	}
}

// TestALateSubagentAggregateDoesNotReshipTheAnswer is the regression that
// retiring a turn's lanes at its end introduced, and the reason a lane is now
// retired only on its own run's terminal.
//
// A subagent's terminal can be shed by the bounded queues that carry it, and
// this projector still projects late lane content after the turn's terminal.
// A lane wiped at turn end is recreated with streamed=false, so the late
// aggregate ships the whole answer the viewer already holds delta by delta.
func TestALateSubagentAggregateDoesNotReshipTheAnswer(t *testing.T) {
	p := NewProjector("sess-1", 0, proseOpts())

	p.Project(rootEvent(events.KindTurnStart, "", "the prompt"))
	p.Project(subagentEvent(events.KindAssistant, "task-1", "the answer", "delta"))
	p.Project(rootEvent(events.KindTurnEnd, "", "completed"))

	// The run's own terminal was shed; its settled aggregate arrives anyway.
	got := p.Project(subagentEvent(events.KindAssistant, "task-1", "the answer", ""))
	if len(got) != 1 {
		t.Fatalf("the late aggregate produced %d wire events, want 1", len(got))
	}
	payload, ok := got[0].Payload.(*SubagentAssistantMessagePayload)
	if !ok {
		t.Fatalf("payload is %T, want *SubagentAssistantMessagePayload", got[0].Payload)
	}
	if payload.Fragments != 1 || payload.Text != "" {
		t.Errorf("Fragments = %d Text = %q, want 1 and empty: the lane forgot it "+
			"had streamed, so the viewer is sent the answer a second time",
			payload.Fragments, payload.Text)
	}
}

// The two tests below are the LANE twins of the root-path tests above. Both
// halves of this work shipped with the root half constrained and the lane half
// not: a review mutated the lane's aggregate condition and the lane's segment
// advance, and the whole package stayed green. A projector that treats a
// subagent's stream as a first-class stream has to be held to the rule on both
// paths, or the rule holds only where someone happened to write a test.

// TestALostLaneResetMakesTheSettledMessageCarryTheAnswer mirrors
// TestALostResetMakesTheSettledMessageCarryTheAnswer for one subagent lane.
func TestALostLaneResetMakesTheSettledMessageCarryTheAnswer(t *testing.T) {
	p := NewProjector("sess-1", 0, proseOpts())

	p.Project(subagentEvent(events.KindAssistant, "task-1", "the first answer", "delta"))
	reset := p.Project(subagentEvent(events.KindAssistantReset, "task-1", "", "schema_retry"))
	if len(reset) != 1 {
		t.Fatalf("the lane reset produced %d wire events, want 1", len(reset))
	}
	p.RollbackStreaming(reset)
	p.Project(subagentEvent(events.KindAssistant, "task-1", "the second answer", "delta"))

	got := p.Project(subagentEvent(events.KindAssistant, "task-1", "the second answer", ""))
	payload, ok := got[0].Payload.(*SubagentAssistantMessagePayload)
	if !ok {
		t.Fatalf("payload is %T, want *SubagentAssistantMessagePayload", got[0].Payload)
	}
	if payload.Text != "the second answer" || payload.Fragments != 0 {
		t.Errorf("Fragments = %d Text = %q, want 0 and the whole answer: this "+
			"lane's viewer still holds the abandoned attempt and has been sent "+
			"nothing that can replace it", payload.Fragments, payload.Text)
	}
}

// TestALaneReplayAfterResetUsesAFreshBlock mirrors
// TestReplayAfterResetUsesAFreshBlock for one subagent lane. Reusing the
// discarded attempt's block puts the replay back where the abandoned attempt
// was, which is the ordering defect the segment advance exists to prevent.
func TestALaneReplayAfterResetUsesAFreshBlock(t *testing.T) {
	p := NewProjector("sess-1", 0, proseOpts())

	first := p.Project(subagentEvent(events.KindAssistant, "task-1", "First try.", "delta"))
	used := blockOf(t, onlyEvent(t, first))
	p.Project(subagentEvent(events.KindAssistantReset, "task-1", "", "retrying"))
	replay := p.Project(subagentEvent(events.KindAssistant, "task-1", "Replayed.", "delta"))

	if got := blockOf(t, onlyEvent(t, replay)); got == used {
		t.Fatalf("the lane's replay reused the discarded attempt's block %q", used)
	}
}

// TestLostDeltaDoesNotSpendAStep: a single assistant delta whose append
// fails, then a tool start, then a delta. The second delta must land in the
// segment the first attempt would have used - nothing shipped, so the tool
// call closed nothing.
func TestLostDeltaDoesNotSpendAStep(t *testing.T) {
	p := NewProjector("sess-1", 0, proseOpts())
	lost := p.Project(rootEvent(events.KindAssistant, "never stored", "delta"))
	wanted := blockOf(t, onlyEvent(t, lost))
	p.RollbackStreaming(lost)
	p.Project(toolEvent(events.KindToolStart, "call-1"))
	got := blockOf(t, onlyEvent(t, p.Project(rootEvent(events.KindAssistant, "stored", "delta"))))
	if got != wanted {
		t.Fatalf("after a lost delta and a tool start the next delta landed in %q, want %q: the lost delta spent a step", got, wanted)
	}
}

// TestThinkingDirtiedSegmentStillAdvancesAfterAssistantRollback: a STORED
// thinking delta plus a LOST assistant delta, then a tool start. The segment
// must advance - the thinking shipped. Fails if the assistant rollback clears
// a flag the thinking stream shares.
func TestThinkingDirtiedSegmentStillAdvancesAfterAssistantRollback(t *testing.T) {
	p := NewProjector("sess-1", 0, proseOpts())
	before := blockOf(t, onlyEvent(t, p.Project(rootEvent(events.KindThinking, "shipped", ""))))
	lost := p.Project(rootEvent(events.KindAssistant, "never stored", "delta"))
	p.RollbackStreaming(lost)
	p.Project(toolEvent(events.KindToolStart, "call-1"))
	after := blockOf(t, onlyEvent(t, p.Project(rootEvent(events.KindThinking, "next step", ""))))
	if stepOf(t, after) == stepOf(t, before) {
		t.Fatalf("segment did not advance past a tool call although thinking had shipped into it (%q)", after)
	}
}

// TestSecondThinkingDeltaLostStillLeavesTheSegmentDirty is the mirrored
// discriminator: thinking delta stored, a second thinking delta lost, tool
// start, prose. The segment DID ship reasoning, so it must advance. Fails
// under any rollback that clears the segment unconditionally.
func TestSecondThinkingDeltaLostStillLeavesTheSegmentDirty(t *testing.T) {
	p := NewProjector("sess-1", 0, proseOpts())
	before := blockOf(t, onlyEvent(t, p.Project(rootEvent(events.KindThinking, "shipped", ""))))
	lost := p.Project(rootEvent(events.KindThinking, "never stored", ""))
	p.RollbackStreaming(lost)
	p.Project(toolEvent(events.KindToolStart, "call-1"))
	after := blockOf(t, onlyEvent(t, p.Project(rootEvent(events.KindAssistant, "prose", "delta"))))
	if stepOf(t, after) == stepOf(t, before) {
		t.Fatalf("segment did not advance (%q): losing the SECOND thinking delta must not forget the first one shipped", after)
	}
}

// TestLostResetDoesNotAdvanceTheStep: a reset whose append fails, then the
// replayed prose. The replay must carry the segment the abandoned text used,
// so a consumer can match the repair to the block it repairs.
func TestLostResetDoesNotAdvanceTheStep(t *testing.T) {
	p := NewProjector("sess-1", 0, proseOpts())
	used := blockOf(t, onlyEvent(t, p.Project(rootEvent(events.KindAssistant, "first try", "delta"))))
	reset := p.Project(rootEvent(events.KindAssistantReset, "", "retrying"))
	p.RollbackStreaming(reset)
	replay := blockOf(t, onlyEvent(t, p.Project(rootEvent(events.KindAssistant, "replayed", "delta"))))
	if replay != used {
		t.Fatalf("replay after a LOST reset landed in %q, want %q: the viewer never heard the reset, so the repair must name the block it holds", replay, used)
	}
}

// TestLostThinkingDeltaDoesNotSpendAStep is the thinking mirror of
// TestLostDeltaDoesNotSpendAStep, for the root stream and for a lane: a
// thinking delta whose append fails, then a tool start, then thinking. The
// step must not have moved - nothing shipped. Without the thinking cases in
// RollbackStreaming the lost delta stays counted and the tool call advances.
func TestLostThinkingDeltaDoesNotSpendAStep(t *testing.T) {
	t.Run("root", func(t *testing.T) {
		p := NewProjector("sess-1", 0, proseOpts())
		lost := p.Project(rootEvent(events.KindThinking, "never stored", ""))
		wanted := blockOf(t, onlyEvent(t, lost))
		p.RollbackStreaming(lost)
		p.Project(toolEvent(events.KindToolStart, "call-1"))
		got := blockOf(t, onlyEvent(t, p.Project(rootEvent(events.KindThinking, "stored", ""))))
		if got != wanted {
			t.Fatalf("after a lost thinking delta and a tool start the next one landed in %q, want %q", got, wanted)
		}
	})
	t.Run("lane", func(t *testing.T) {
		p := NewProjector("sess-1", 0, proseOpts())
		lost := p.Project(subagentEvent(events.KindThinking, "task-a", "never stored", ""))
		wanted := blockOf(t, onlyEvent(t, lost))
		p.RollbackStreaming(lost)
		laneTool := events.Event{Kind: events.KindSubagentStart, SessionID: "sess-1", TurnID: "turn:1", Timestamp: time.Now(), ToolCallID: "lane-call", Name: "Read"}
		p.Project(laneTool.WithAgentAttribution("task-a", "builder", 1))
		got := blockOf(t, onlyEvent(t, p.Project(subagentEvent(events.KindThinking, "task-a", "stored", ""))))
		if got != wanted {
			t.Fatalf("after a lost lane thinking delta and the lane's tool start the next one landed in %q, want %q", got, wanted)
		}
	})
}

// TestLostResetOnACleanSegmentRestoresNothing pins the conditionality of
// the reset undo. A reset before anything shipped advances nothing, so its
// lost append must restore nothing either: the next prose lands exactly
// where a control that never saw the reset puts it.
func TestLostResetOnACleanSegmentRestoresNothing(t *testing.T) {
	subject := NewProjector("sess-1", 0, proseOpts())
	reset := subject.Project(rootEvent(events.KindAssistantReset, "", "retrying"))
	subject.RollbackStreaming(reset)
	got := blockOf(t, onlyEvent(t, subject.Project(rootEvent(events.KindAssistant, "answer", "delta"))))

	control := NewProjector("sess-1", 0, proseOpts())
	want := blockOf(t, onlyEvent(t, control.Project(rootEvent(events.KindAssistant, "answer", "delta"))))
	if got != want {
		t.Fatalf("after a lost reset on a clean segment the delta landed in %q, want %q: the undo restored a step it never replaced", got, want)
	}
	if stepOf(t, got) < 0 {
		t.Fatalf("step under-ran: %q", got)
	}
}
