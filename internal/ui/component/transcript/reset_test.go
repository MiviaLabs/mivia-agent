package transcript

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// allText joins every prose line the model holds, which is what a reader of
// the transcript actually sees.
func allText(m Model) string {
	var sb strings.Builder
	for _, b := range m.blocks {
		sb.WriteString(strings.Join(b.Body, "\n"))
		sb.WriteString("\n")
		sb.WriteString(b.Header.Detail)
		sb.WriteString("\n")
	}
	sb.WriteString(m.pending)
	return sb.String()
}

// TestResetDiscardsTheRejectedAnswer is the reported defect on the TUI.
//
// A schema retry re-drives the turn and sends the whole answer again. Before
// the reset had an arm here, the rejected attempt stayed on screen with its
// replacement appended under it, and nothing said which one the agent acted
// on.
func TestResetDiscardsTheRejectedAnswer(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)

	m, _ = m.HandleEvent(uievent.Event{Body: uievent.TextEndBody{Text: "the rejected reply"}})
	m, _ = m.HandleEvent(uievent.Event{Body: uievent.AssistantResetBody{Reason: "schema_retry"}})
	m, _ = m.HandleEvent(uievent.Event{Body: uievent.TextEndBody{Text: "the accepted reply"}})

	got := allText(m)
	if strings.Contains(got, "the rejected reply") {
		t.Errorf("the rejected attempt is still on screen alongside its replacement:\n%s", got)
	}
	if !strings.Contains(got, "the accepted reply") {
		t.Errorf("the replacement is missing; the reset removed more than the attempt:\n%s", got)
	}
}

// TestResetDiscardsAnAnswerStillStreaming covers the retry that fires before
// the answer settles: the text lives in the pending span, not in a block.
func TestResetDiscardsAnAnswerStillStreaming(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)

	m, _ = m.HandleEvent(uievent.Event{Body: uievent.TextDeltaBody{Text: "half an ans"}})
	m, _ = m.HandleEvent(uievent.Event{Body: uievent.AssistantResetBody{}})

	if m.pending != "" {
		t.Errorf("the in-flight span survived the reset: %q", m.pending)
	}
	if got := allText(m); strings.Contains(got, "half an ans") {
		t.Errorf("the partial answer was committed instead of discarded:\n%s", got)
	}
}

// TestResetKeepsTheWorkThatReallyHappened is the limit on what it removes.
//
// A retry re-drives the ANSWER. The tool calls before it ran, produced real
// effects, and are not replayed; removing them would delete the record of the
// very work the reader needs to judge the retry.
func TestResetKeepsToolWorkAndOnlyDropsTrailingProse(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)

	m, _ = m.HandleEvent(uievent.Event{Body: uievent.ToolEndBody{
		ToolCallID: "c1", Name: "read_file", OK: true, Result: "the file",
	}})
	m, _ = m.HandleEvent(uievent.Event{Body: uievent.TextEndBody{Text: "the rejected reply"}})
	m, _ = m.HandleEvent(uievent.Event{Body: uievent.AssistantResetBody{Reason: "schema_retry"}})

	var sawTool bool
	for _, k := range blockKinds(m) {
		if k == uievent.KindToolEnd {
			sawTool = true
		}
		if k == uievent.KindTextEnd {
			t.Errorf("a rejected prose block survived; blocks = %v", blockKinds(m))
		}
	}
	if !sawTool {
		t.Errorf("the reset removed the tool call that preceded the answer; blocks = %v", blockKinds(m))
	}
}

// TestResetSaysSomethingWasDiscarded holds the transcript to its own rule:
// text vanishing with no explanation is the transcript lying about what
// happened.
func TestResetSaysSomethingWasDiscarded(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)

	m, _ = m.HandleEvent(uievent.Event{Body: uievent.TextEndBody{Text: "the rejected reply"}})
	m, _ = m.HandleEvent(uievent.Event{Body: uievent.AssistantResetBody{Reason: "schema_retry"}})

	var sawNotice bool
	for _, b := range m.blocks {
		if b.Kind == uievent.KindNotice && strings.Contains(b.Header.Detail, "discarded") {
			sawNotice = true
			if !strings.Contains(b.Header.Detail, "schema_retry") {
				t.Errorf("the notice drops the reason: %q", b.Header.Detail)
			}
		}
	}
	if !sawNotice {
		t.Errorf("text disappeared with no line saying why; blocks = %v", blockKinds(m))
	}
}

// TestResetWithNothingToDiscardIsSilent is the other half of that rule: a
// line about a discard that removed nothing is the transcript inventing an
// event.
func TestResetWithNothingToDiscardIsSilent(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)

	m, _ = m.HandleEvent(uievent.Event{Body: uievent.AssistantResetBody{Reason: "schema_retry"}})

	if len(m.blocks) != 0 {
		t.Errorf("a reset with nothing on screen pushed %v", blockKinds(m))
	}
}
