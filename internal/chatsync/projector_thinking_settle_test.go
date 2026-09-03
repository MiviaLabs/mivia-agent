package chatsync

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/redact"
)

// installPolicy activates a redaction policy for one test and restores the
// previous one. Every test here needs it: the defect only exists when
// redact.Active() is true, which is whenever the operator configured ANY
// key name or pattern.
func installPolicy(t *testing.T) {
	t.Helper()
	pol, err := redact.Compile([]string{`sk-live-[A-Za-z0-9]{10,}`}, nil, "[redacted]")
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	old := redact.Current()
	redact.SetPolicy(pol)
	t.Cleanup(func() { redact.SetPolicy(old) })
}

// firstOfType returns the first wire event of the given type, or fails.
func firstOfType(t *testing.T, got []WireEvent, want string) WireEvent {
	t.Helper()
	for _, ev := range got {
		if ev.Type == want {
			return ev
		}
	}
	types := make([]string, 0, len(got))
	for _, ev := range got {
		types = append(types, ev.Type)
	}
	t.Fatalf("no %s in %v", want, types)
	return WireEvent{}
}

// TestThinkingReachesTheWireUnderAPolicyAsOneAggregate is the decisive
// regression for the defect this type exists to close.
//
// With a redaction policy active - which is EVERY session for an operator who
// configured one key name - the per-fragment thinking text is withheld, by
// design: a secret split across two deltas matches neither pattern. Before the
// settled aggregate that was the end of it. The deltas shipped bytes and no
// text, the web consumer dropped the empty block as empty prose, and the
// agent's reasoning was absent from the transcript entirely while the CLI
// showed it.
//
// The aggregate carries the whole block, redacted as ONE string, which is both
// the recovery and the reason the per-fragment suppression can stay.
func TestThinkingReachesTheWireUnderAPolicyAsOneAggregate(t *testing.T) {
	installPolicy(t)
	p := NewProjector("sess-1", 0, proseOpts())

	frags := []string{"the key is sk-live-", "AbCdEfGhIjKl", " and I will use it"}
	for _, frag := range frags {
		got := p.Project(rootEvent(events.KindThinking, frag, ""))
		if payload := got[0].Payload.(*ThinkingDeltaPayload); payload.Text != "" {
			t.Fatalf("a fragment shipped text under a policy: %q", payload.Text)
		}
	}

	// A tool call closes the block: the model stopped reasoning and acted.
	settled := firstOfType(t, p.Project(rootEvent(events.KindToolStart, "", "")), TypeThinkingMessage)
	payload := settled.Payload.(*ThinkingMessagePayload)

	whole := strings.Join(frags, "")
	if payload.Text == "" {
		t.Fatal("the settled aggregate carried no text - the reasoning is still lost under a policy")
	}
	if strings.Contains(payload.Text, "sk-live-AbCdEfGhIjKl") {
		t.Errorf("the aggregate leaked the key: %q", payload.Text)
	}
	if !strings.Contains(payload.Text, "[redacted]") {
		t.Errorf("text = %q, want the placeholder - the whole-block redaction did not run", payload.Text)
	}
	// The pattern spans fragment one and two. It can only have matched
	// because the aggregate redacted the JOINED text, which is the point.
	if !strings.HasPrefix(payload.Text, "the key is ") || !strings.HasSuffix(payload.Text, " and I will use it") {
		t.Errorf("text = %q, want the whole block's reasoning around the placeholder", payload.Text)
	}
	if payload.Fragments != 0 {
		t.Errorf("fragments = %d, want 0 - no fragment's text reached the wire", payload.Fragments)
	}
	if payload.Bytes != len(whole) {
		t.Errorf("bytes = %d, want %d", payload.Bytes, len(whole))
	}
	if payload.Status != "completed" {
		t.Errorf("status = %q, want completed", payload.Status)
	}
}

// TestThinkingAggregateNamesTheBlockItsDeltasUsed pins the block id. The
// aggregate settles from a LATER event whose own step has moved on, so naming
// the current segment would file the reasoning under a block that holds
// nothing - and the consumer keys its prose blocks on exactly this string.
func TestThinkingAggregateNamesTheBlockItsDeltasUsed(t *testing.T) {
	installPolicy(t)
	p := NewProjector("sess-1", 0, proseOpts())

	delta := p.Project(rootEvent(events.KindThinking, "reasoning", ""))[0]
	deltaBlock := delta.Payload.(*ThinkingDeltaPayload).Block

	settled := firstOfType(t, p.Project(rootEvent(events.KindToolStart, "", "")), TypeThinkingMessage)
	if got := settled.Payload.(*ThinkingMessagePayload).Block; got != deltaBlock {
		t.Errorf("aggregate block = %q, want the delta's %q", got, deltaBlock)
	}
}

// TestThinkingAggregatePrecedesTheToolThatClosedIt pins the wire order. A
// viewer renders in arrival order, so an aggregate emitted after its tool
// event would show the reasoning BELOW the card it explains.
func TestThinkingAggregatePrecedesTheToolThatClosedIt(t *testing.T) {
	installPolicy(t)
	p := NewProjector("sess-1", 0, proseOpts())

	p.Project(rootEvent(events.KindThinking, "reasoning", ""))
	got := p.Project(rootEvent(events.KindToolStart, "", ""))
	if len(got) < 2 {
		t.Fatalf("tool start produced %d events, want the aggregate and the tool", len(got))
	}
	if got[0].Type != TypeThinkingMessage {
		t.Errorf("first event = %q, want the settled aggregate before the tool", got[0].Type)
	}
	if got[1].Type != TypeToolStarted {
		t.Errorf("second event = %q, want the tool start", got[1].Type)
	}
}

// TestThinkingSettlesOncePerBlock proves each STEP settles separately. A turn
// that reasons, calls a tool, and reasons again fills two blocks; one
// aggregate covering both would put the second step's reasoning in the first
// step's bubble.
func TestThinkingSettlesOncePerBlock(t *testing.T) {
	installPolicy(t)
	p := NewProjector("sess-1", 0, proseOpts())

	p.Project(rootEvent(events.KindThinking, "first step", ""))
	first := firstOfType(t, p.Project(rootEvent(events.KindToolStart, "", "")), TypeThinkingMessage)
	p.Project(rootEvent(events.KindToolEnd, "", "ok"))
	p.Project(rootEvent(events.KindThinking, "second step", ""))
	second := firstOfType(t, p.Project(rootEvent(events.KindTurnEnd, "", "")), TypeThinkingMessage)

	a := first.Payload.(*ThinkingMessagePayload)
	b := second.Payload.(*ThinkingMessagePayload)
	if a.Text != "first step" {
		t.Errorf("first aggregate text = %q, want %q", a.Text, "first step")
	}
	if b.Text != "second step" {
		t.Errorf("second aggregate text = %q, want %q - a block leaked into the next", b.Text, "second step")
	}
	if a.Block == b.Block {
		t.Errorf("both aggregates named block %q - two steps must be two blocks", a.Block)
	}
}

// TestThinkingSettlesAtTurnEnd covers the step that thought and then finished
// without calling a tool. Nothing else closes that block, so without a settle
// here the last thing the agent reasoned about is dropped with the state.
func TestThinkingSettlesAtTurnEnd(t *testing.T) {
	installPolicy(t)
	p := NewProjector("sess-1", 0, proseOpts())

	p.Project(rootEvent(events.KindTurnStart, "", ""))
	p.Project(rootEvent(events.KindThinking, "final reasoning", ""))
	got := p.Project(rootEvent(events.KindTurnEnd, "", ""))

	settled := firstOfType(t, got, TypeThinkingMessage)
	if text := settled.Payload.(*ThinkingMessagePayload).Text; text != "final reasoning" {
		t.Errorf("text = %q, want the turn's last reasoning", text)
	}
	if got[len(got)-1].Type != TypeTurnEnded {
		t.Errorf("last event = %q, want the turn end to come after the settle", got[len(got)-1].Type)
	}
}

// TestThinkingAggregateObeysINV1WhenFragmentsShipped is the other half of the
// invariant. With NO policy the deltas carry the text, the viewer has already
// stitched it, and repeating it in the aggregate would double the reasoning.
func TestThinkingAggregateObeysINV1WhenFragmentsShipped(t *testing.T) {
	old := redact.Current()
	redact.SetPolicy(nil)
	t.Cleanup(func() { redact.SetPolicy(old) })

	p := NewProjector("sess-1", 0, proseOpts())
	p.Project(rootEvent(events.KindThinking, "one ", ""))
	p.Project(rootEvent(events.KindThinking, "two", ""))

	settled := firstOfType(t, p.Project(rootEvent(events.KindToolStart, "", "")), TypeThinkingMessage)
	payload := settled.Payload.(*ThinkingMessagePayload)
	if payload.Fragments != 2 {
		t.Errorf("fragments = %d, want 2", payload.Fragments)
	}
	if payload.Text != "" {
		t.Errorf("text = %q, want empty - INV-1 says the deltas carry it", payload.Text)
	}
	if payload.Bytes != len("one two") {
		t.Errorf("bytes = %d, want %d", payload.Bytes, len("one two"))
	}
}

// TestThinkingAggregateWithholdsTextWhenThinkingIsOff proves the aggregate
// respects the host's own switch. IncludeThinking off means the operator asked
// for no reasoning text anywhere; a new event type is not an exemption from
// that. Bytes still ship, exactly as the deltas do.
func TestThinkingAggregateWithholdsTextWhenThinkingIsOff(t *testing.T) {
	installPolicy(t)
	p := NewProjector("sess-1", 0, ProjectorOptions{StreamAssistant: true})

	p.Project(rootEvent(events.KindThinking, "private reasoning", ""))
	settled := firstOfType(t, p.Project(rootEvent(events.KindToolStart, "", "")), TypeThinkingMessage)
	payload := settled.Payload.(*ThinkingMessagePayload)
	if payload.Text != "" {
		t.Errorf("text = %q, want empty when IncludeThinking is off", payload.Text)
	}
	if payload.Bytes != len("private reasoning") {
		t.Errorf("bytes = %d, want the real size", payload.Bytes)
	}
}

// TestNoThinkingAggregateWithoutThinking proves the settle is conditional. A
// turn that never reasoned must not emit an empty aggregate before every tool
// call it makes - the consumer would render a bubble per call.
func TestNoThinkingAggregateWithoutThinking(t *testing.T) {
	installPolicy(t)
	p := NewProjector("sess-1", 0, proseOpts())

	for _, got := range [][]WireEvent{
		p.Project(rootEvent(events.KindToolStart, "", "")),
		p.Project(rootEvent(events.KindTurnEnd, "", "")),
	} {
		for _, ev := range got {
			if ev.Type == TypeThinkingMessage {
				t.Fatalf("an aggregate shipped for a block that never opened: %+v", ev.Payload)
			}
		}
	}
}

// TestSubagentThinkingSettlesOnItsOwnLane covers the subagent lane, which has
// its own copy of every prose decision. A redaction policy is session-wide, so
// a lane's reasoning is withheld under precisely the conditions the root's is
// and needs precisely the same recovery.
//
// It also pins the ATTRIBUTION: the aggregate must use the subagent type and
// name the lane's block, or a subagent's reasoning lands in the main
// transcript that a prefix-filtering viewer is trying to keep it out of.
func TestSubagentThinkingSettlesOnItsOwnLane(t *testing.T) {
	installPolicy(t)
	p := NewProjector("sess-1", 0, proseOpts())

	delta := p.Project(subagentEvent(events.KindThinking, "task-1", "lane sk-live-", ""))[0]
	p.Project(subagentEvent(events.KindThinking, "task-1", "AbCdEfGhIjKl now", ""))
	deltaBlock := delta.Payload.(*SubagentThinkingDeltaPayload).Block

	got := p.Project(subagentEvent(events.KindToolStart, "task-1", "", ""))
	for _, ev := range got {
		if ev.Type == TypeThinkingMessage {
			t.Fatalf("a lane settled under the ROOT type: %+v", ev.Payload)
		}
	}
	settled := firstOfType(t, got, TypeSubagentThinkingMessage)
	payload := settled.Payload.(*SubagentThinkingMessagePayload)

	if payload.Block != deltaBlock {
		t.Errorf("aggregate block = %q, want the lane delta's %q", payload.Block, deltaBlock)
	}
	if payload.Agent == nil || payload.Agent.Task != "task-1" {
		t.Errorf("aggregate lost its agent origin: %+v", payload.Agent)
	}
	if strings.Contains(payload.Text, "sk-live-AbCdEfGhIjKl") {
		t.Errorf("the lane aggregate leaked the key: %q", payload.Text)
	}
	if !strings.Contains(payload.Text, "[redacted]") {
		t.Errorf("lane text = %q, want the whole-block redaction", payload.Text)
	}
	if payload.Fragments != 0 {
		t.Errorf("fragments = %d, want 0 - nothing streamed under a policy", payload.Fragments)
	}
}

// TestRootThinkingIsNotSettledByASubagentsToolCall pins the lane split from
// the other side. Two runs stream at once; a subagent acting must not close
// the root's open reasoning, which would file the root's text under whatever
// segment the lane happened to be in.
func TestRootThinkingIsNotSettledByASubagentsToolCall(t *testing.T) {
	installPolicy(t)
	p := NewProjector("sess-1", 0, proseOpts())

	p.Project(rootEvent(events.KindThinking, "root reasoning", ""))
	for _, ev := range p.Project(subagentEvent(events.KindToolStart, "task-1", "", "")) {
		if ev.Type == TypeThinkingMessage {
			t.Fatalf("a subagent's tool call settled the ROOT thinking block: %+v", ev.Payload)
		}
	}
	// It is still open, and the root's own boundary settles it.
	settled := firstOfType(t, p.Project(rootEvent(events.KindToolStart, "", "")), TypeThinkingMessage)
	if text := settled.Payload.(*ThinkingMessagePayload).Text; text != "root reasoning" {
		t.Errorf("text = %q, want the root's own reasoning", text)
	}
}
