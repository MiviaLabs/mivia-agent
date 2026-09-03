package chatsync

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/redact"
)

// Streaming under an active redaction policy.
//
// Before this, ANY configured rule - one key name is enough to make
// redact.Active() true for the whole session - stopped every assistant delta
// and blanked every thinking delta. The transcript arrived in one lump at the
// end of the turn. The reason was sound: a regex over one fragment cannot see a
// secret that spans two. The repair is to move the redaction boundary, not to
// remove the stream: redact.Stream holds a bounded tail so the boundary spans
// fragments, and the projector flushes that tail at every block close.
//
// These tests state the property at the projector's own level, because the
// producer is where a viewer's bytes are decided.

// windowPattern keeps the last 256 bytes of a block open as a possible match
// and never completes one: its partial match is alive for exactly that many
// bytes, and the NUL-led marker occurs in no fixture. The hold-back used to be
// a flat window of that size; it is now content-aware, so ordinary prose ships
// at once. The flush, settle and rollback tests here were written against a
// held tail and need one, so they install this pattern alongside their real
// one - it models an operator rule whose partial match stays open, which is
// the shape the hold still exists for.
const windowPattern = "(?s).{0,256}\x00window\x00"

// windowedPolicy installs the given patterns plus windowPattern for one test.
func windowedPolicy(t *testing.T, patterns ...string) {
	t.Helper()
	pol, err := redact.Compile(append(patterns, windowPattern), nil, "[redacted]")
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	old := redact.Current()
	redact.SetPolicy(pol)
	t.Cleanup(func() { redact.SetPolicy(old) })
}

// streamPolicy installs a windowed policy that catches a synthetic credential
// shape (a real vendor prefix here would trip the repo's own secret scanner).
func streamPolicy(t *testing.T) {
	t.Helper()
	windowedPolicy(t, `xk-tok-[A-Za-z0-9-]{8,64}`)
}

// TestOrdinaryProseShipsAtOnceUnderAPolicy pins the latency half of the trade
// at the producer: with a real credential rule and no window, a four-byte
// delta of prose is on the wire in the same Project call, not held until the
// block closes.
func TestOrdinaryProseShipsAtOnceUnderAPolicy(t *testing.T) {
	pol, err := redact.Compile([]string{`xk-tok-[A-Za-z0-9-]{8,64}`}, nil, "[redacted]")
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	old := redact.Current()
	redact.SetPolicy(pol)
	t.Cleanup(func() { redact.SetPolicy(old) })

	p := NewProjector("sess-1", 0, proseOpts())
	got := p.Project(rootEvent(events.KindAssistant, "The ", "delta"))
	if text := shippedText(got); text != "The " {
		t.Fatalf("a prose delta shipped %q under a policy, want it whole and at once", text)
	}
}

// shippedText concatenates the text of every delta payload of one type.
func shippedText(evs []WireEvent) string {
	var out strings.Builder
	for _, we := range evs {
		switch payload := we.Payload.(type) {
		case *AssistantDeltaPayload:
			out.WriteString(payload.Text)
		case *SubagentAssistantDeltaPayload:
			out.WriteString(payload.Text)
		case *ThinkingDeltaPayload:
			out.WriteString(payload.Text)
		case *SubagentThinkingDeltaPayload:
			out.WriteString(payload.Text)
		}
	}
	return out.String()
}

// TestAssistantDeltasShipUnderAPolicyWithASplitSecretRedacted is the decisive
// test for the producer. The secret is cut in half at the delta boundary, which
// is exactly what a per-fragment redactor cannot see.
func TestAssistantDeltasShipUnderAPolicyWithASplitSecretRedacted(t *testing.T) {
	streamPolicy(t)
	p := NewProjector("sess-1", 0, proseOpts())

	pad := strings.Repeat("some ordinary narration. ", 26)

	var out []WireEvent
	out = append(out, p.Project(rootEvent(events.KindAssistant, pad+"the key is xk-tok-", "delta"))...)
	out = append(out, p.Project(rootEvent(events.KindAssistant, "api03-AAAABBBBCCCCDDDD and that is all", "delta"))...)
	out = append(out, p.Project(rootEvent(events.KindTurnEnd, "", "completed"))...)

	got := shippedText(out)
	if strings.Contains(got, "xk-tok") {
		t.Fatalf("a secret split across two deltas reached the wire: %q", got)
	}
	if want := pad + "the key is [redacted] and that is all"; got != want {
		t.Errorf("shipped deltas concatenate to %q, want %q", got, want)
	}
	if got == "" {
		t.Error("nothing streamed at all - the policy suppressed the live transcript, " +
			"which is the defect this change removes")
	}
}

// TestThinkingDeltasCarryTextUnderAPolicy pins the same repair on the reasoning
// lane, which shipped bytes with an empty text under any policy at all.
func TestThinkingDeltasCarryTextUnderAPolicy(t *testing.T) {
	streamPolicy(t)
	p := NewProjector("sess-1", 0, proseOpts())

	// Padded past the hold-back window, because that is the whole point: text
	// beyond the window streams live, and only the trailing window waits for
	// the block to close.
	pad := strings.Repeat("weighing the options. ", 30)

	var out []WireEvent
	out = append(out, p.Project(rootEvent(events.KindThinking, pad+"I should use xk-tok-", ""))...)
	out = append(out, p.Project(rootEvent(events.KindThinking, "api03-AAAABBBBCCCCDDDD next", ""))...)
	out = append(out, p.Project(rootEvent(events.KindTurnEnd, "", "completed"))...)

	got := shippedText(out)
	if got == "" {
		t.Fatal("every thinking delta shipped an empty text - the policy blanked the " +
			"reasoning stream, which is the defect this change removes")
	}
	if strings.Contains(got, "xk-tok") {
		t.Fatalf("a split secret reached the wire in thinking deltas: %q", got)
	}
	if want := pad + "I should use [redacted] next"; got != want {
		t.Errorf("thinking deltas concatenate to %q, want %q", got, want)
	}
}

// TestNoAssistantTextIsLostWhenABlockClosesMidHold is the other half of the
// trade. The hold-back delays text; if a block close forgot to flush it, the
// delay would become deletion, and the reader could not tell.
func TestNoAssistantTextIsLostWhenABlockClosesMidHold(t *testing.T) {
	streamPolicy(t)
	p := NewProjector("sess-1", 0, proseOpts())

	// Long enough that the first delta ships something and leaves a tail.
	first := strings.Repeat("a first sentence that is quite long. ", 12)
	second := "a short trailing clause"

	var out []WireEvent
	out = append(out, p.Project(rootEvent(events.KindAssistant, first, "delta"))...)
	out = append(out, p.Project(rootEvent(events.KindAssistant, second, "delta"))...)
	// A tool call closes the block while a tail is still held.
	out = append(out, p.Project(rootEvent(events.KindToolStart, "", ""))...)

	if got, want := shippedText(out), first+second; got != want {
		t.Fatalf("the block close shipped %d bytes, want %d - a held tail was "+
			"dropped, which loses text outright", len(got), len(want))
	}
}

// TestStreamingUnderAPolicyKeepsINV1 pins the invariant a consumer stitches on:
// the settled aggregate's text is empty exactly when fragments shipped, so the
// reader is never shown the same prose twice.
func TestStreamingUnderAPolicyKeepsINV1(t *testing.T) {
	streamPolicy(t)
	p := NewProjector("sess-1", 0, proseOpts())

	whole := strings.Repeat("plain prose. ", 40)
	p.Project(rootEvent(events.KindAssistant, whole, "delta"))
	out := p.Project(rootEvent(events.KindAssistant, whole, ""))

	var msg *AssistantMessagePayload
	for _, we := range out {
		if m, ok := we.Payload.(*AssistantMessagePayload); ok {
			msg = m
		}
	}
	if msg == nil {
		t.Fatal("no settled assistant message")
	}
	if msg.Fragments == 0 {
		t.Fatalf("Fragments = 0 though deltas shipped: %q", shippedText(out))
	}
	if msg.Text != "" {
		t.Errorf("Text = %q with Fragments = %d - INV-1 says text is empty exactly "+
			"when fragments shipped, or the viewer renders the answer twice",
			msg.Text, msg.Fragments)
	}
	if got := shippedText(out); !strings.HasSuffix(whole, got) {
		t.Errorf("the flushed tail %q is not the end of the answer", got)
	}
}

// TestASubagentLaneStreamsUnderAPolicyToo keeps the lane at the root's
// standard, in both directions: it must stream, and its split secret must be
// caught. A policy is session-wide, so a divergence here is a silent hole in
// exactly the transcript nobody is watching live.
func TestASubagentLaneStreamsUnderAPolicyToo(t *testing.T) {
	streamPolicy(t)
	p := NewProjector("sess-1", 0, proseOpts())

	pad := strings.Repeat("the lane narrates its work. ", 24)
	whole := pad + "lane key xk-tok-api03-AAAABBBBCCCCDDDD end"

	var out []WireEvent
	out = append(out, p.Project(subagentEvent(events.KindAssistant, "task-1", pad+"lane key xk-tok-", "delta"))...)
	out = append(out, p.Project(subagentEvent(events.KindAssistant, "task-1", "api03-AAAABBBBCCCCDDDD end", "delta"))...)
	out = append(out, p.Project(subagentEvent(events.KindAssistant, "task-1", whole, ""))...)

	got := shippedText(out)
	if got == "" {
		t.Fatal("the lane streamed nothing under a policy while the root streams - " +
			"a session-wide policy must not treat the two differently")
	}
	if strings.Contains(got, "xk-tok") {
		t.Fatalf("a lane's split secret reached the wire: %q", got)
	}
	if want := pad + "lane key [redacted] end"; got != want {
		t.Errorf("lane deltas concatenate to %q, want %q", got, want)
	}
}

// TestAnAssistantResetDropsTheHeldTail is the one place a held tail may be
// discarded: the consumer is being told to throw the block away, so shipping
// the tail would deliver words into text that is about to be erased.
func TestAnAssistantResetDropsTheHeldTail(t *testing.T) {
	streamPolicy(t)
	p := NewProjector("sess-1", 0, proseOpts())

	p.Project(rootEvent(events.KindAssistant, "abandoned attempt", "delta"))
	p.Project(rootEvent(events.KindAssistantReset, "", "retry"))
	out := p.Project(rootEvent(events.KindTurnEnd, "", "completed"))

	if got := shippedText(out); got != "" {
		t.Errorf("the turn's end shipped %q from a block the consumer was told to "+
			"discard", got)
	}
}
