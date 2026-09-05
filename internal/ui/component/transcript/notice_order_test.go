package transcript

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// TestMidStreamNoticeDoesNotDuplicateTheAnswer guards the NoticeBody arm's
// deliberate omission of flushPending.
//
// Every sibling arm in HandleEvent flushes the pending span before pushing
// its block, and the notice arm's failure to do so reads as an ordering bug
// now that ports.Notices delivers workflow progress mid-turn. Adding the
// flush is the obvious "fix" and it is wrong: uiadapter's translateAssistant
// sends the FULL accumulated answer on text.end, not the remaining segment,
// so committing the partial span here renders the answer twice - once
// truncated at whatever token the notice happened to interrupt.
//
// This test fails if someone adds the flush.
func TestMidStreamNoticeDoesNotDuplicateTheAnswer(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 24)

	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindTextDelta, Body: uievent.TextDeltaBody{Text: "starting the "}})
	m, _ = m.HandleEvent(noticeEvent("workflow wfr-1: step build started"))
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindTextEnd, Body: uievent.TextEndBody{Text: "starting the run now"}})

	blocks := m.Blocks()
	if len(blocks) != 2 {
		t.Fatalf("got %d blocks, want exactly the notice and the one finished answer", len(blocks))
	}
	if n := strings.Count(ansi.Strip(m.View()), "starting the "); n != 1 {
		t.Fatalf("the answer appears %d times, want 1: flushing the pending span commits a truncated copy that text.end then repeats", n)
	}
}

// TestNoticeBetweenTurnsLandsAsOneBlock pins the common case: a notice with
// no span open is one block, in order, with nothing else disturbed.
func TestNoticeBetweenTurnsLandsAsOneBlock(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 24)

	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindTextEnd, Body: uievent.TextEndBody{Text: "the answer"}})
	m, _ = m.HandleEvent(noticeEvent("workflow wfr-1: finished"))

	blocks := m.Blocks()
	if len(blocks) != 2 {
		t.Fatalf("got %d blocks, want the answer and the notice", len(blocks))
	}
	if blocks[1].Kind != uievent.KindNotice {
		t.Fatalf("got %v as the last block, want the notice after the answer that preceded it", blocks[1].Kind)
	}
}
