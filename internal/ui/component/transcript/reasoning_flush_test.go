package transcript

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// blockKinds lists the kinds of every block the model currently holds, in
// order, so a test can state what a reader would actually see.
func blockKinds(m Model) []uievent.Kind {
	out := make([]uievent.Kind, 0, len(m.blocks))
	for _, b := range m.blocks {
		out = append(out, b.Kind)
	}
	return out
}

func blockText(m Model, kind uievent.Kind) string {
	for _, b := range m.blocks {
		if b.Kind == kind {
			return strings.Join(b.Body, "\n")
		}
	}
	return ""
}

// TestReasoningSurvivesAnAnswerWithNoToolCallBetween is the reported bug.
//
// Reasoning deltas accumulate into a PENDING span that only becomes a block
// when something flushes it, and the flush was driven by tool and turn
// events. text.end discarded the pending span outright. An agent that reasons
// and then answers - with no tool call in between, which is the ordinary
// shape of a subagent run - therefore had its whole reasoning block wiped the
// moment its answer arrived. Reopening the thread showed the reasoning again,
// because history replay takes a different branch, which is what made the
// loss look random rather than systematic.
func TestReasoningSurvivesAnAnswerWithNoToolCallBetween(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)

	m, _ = m.HandleEvent(uievent.Event{Body: uievent.ReasoningDeltaBody{Text: "weighing "}})
	m, _ = m.HandleEvent(uievent.Event{Body: uievent.ReasoningDeltaBody{Text: "the options"}})
	m, _ = m.HandleEvent(uievent.Event{Body: uievent.TextEndBody{Text: "the answer"}})

	kinds := blockKinds(m)
	var sawReasoning, sawText bool
	for _, k := range kinds {
		if k == uievent.KindReasoning {
			sawReasoning = true
		}
		if k == uievent.KindTextEnd {
			sawText = true
		}
	}
	if !sawReasoning {
		t.Errorf("reasoning was discarded by the answer; blocks = %v", kinds)
	}
	if !sawText {
		t.Errorf("the answer is missing; blocks = %v", kinds)
	}
	if got := blockText(m, uievent.KindReasoning); got != "weighing the options" {
		t.Errorf("reasoning text = %q, want the accumulated deltas", got)
	}
}

// TestAnswerIsNotRenderedTwice guards the other side of the same edit. A
// text.end carries the FULL accumulated answer, so flushing a pending TEXT
// span alongside it would show the answer once from the pending buffer and
// again from the event.
func TestAnswerIsNotRenderedTwice(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)

	m, _ = m.HandleEvent(uievent.Event{Body: uievent.TextDeltaBody{Text: "the "}})
	m, _ = m.HandleEvent(uievent.Event{Body: uievent.TextDeltaBody{Text: "answer"}})
	m, _ = m.HandleEvent(uievent.Event{Body: uievent.TextEndBody{Text: "the answer"}})

	var textBlocks int
	for _, k := range blockKinds(m) {
		if k == uievent.KindTextEnd {
			textBlocks++
		}
	}
	if textBlocks != 1 {
		t.Errorf("answer rendered in %d blocks, want exactly 1", textBlocks)
	}
}

// TestReasoningDoesNotBleedIntoTheAnswer proves a change of pending kind ends
// the previous span. The two share one buffer, so without a flush on the
// change the reasoning text concatenated into the answer and was presented as
// the model's reply.
func TestReasoningDoesNotBleedIntoTheAnswer(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)

	m, _ = m.HandleEvent(uievent.Event{Body: uievent.ReasoningDeltaBody{Text: "private thought"}})
	m, _ = m.HandleEvent(uievent.Event{Body: uievent.TextDeltaBody{Text: "public answer"}})
	m, _ = m.HandleEvent(uievent.Event{Body: uievent.TextEndBody{Text: "public answer"}})

	if got := blockText(m, uievent.KindReasoning); !strings.Contains(got, "private thought") {
		t.Errorf("reasoning block = %q, want the reasoning text", got)
	}
	answer := blockText(m, uievent.KindTextEnd)
	if strings.Contains(answer, "private thought") {
		t.Errorf("answer = %q, carries reasoning text that was never part of the reply", answer)
	}
}
