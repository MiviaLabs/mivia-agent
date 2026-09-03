package conversation

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// longThreadHistory builds enough thread history to push the dialog
// transcript past its viewport, so the scroll keys have somewhere to
// move.
func longThreadHistory(turns int) []ports.Message {
	msgs := make([]ports.Message, 0, turns*2)
	for i := 0; i < turns; i++ {
		msgs = append(msgs,
			ports.Message{Role: "user", Text: fmt.Sprintf("analysis request %d: %s", i, strings.Repeat("detail line ", 14))},
			ports.Message{Role: "assistant", Text: fmt.Sprintf("finding %d: %s", i, strings.Repeat("result line ", 14))},
		)
	}
	return msgs
}

// TestSubagentThreadDialogKeys_RunningSubagentIsReadOnlyToo pins the
// same read-only contract for a subagent still RUNNING (non-terminal
// status) as TestSubagentHistoryDialog_ScrollingAndKeyHandlingWhenComposerHidden
// pins for a finished one: the composer is hidden from the moment the
// dialog opens (see openThread), j/k/arrows scroll the transcript one
// line per press, and no key ever reaches the composer. There is no
// "visible, live composer" state left to route keys into - the operator
// has no channel to a subagent's own conversation either way.
func TestSubagentThreadDialogKeys_RunningSubagentIsReadOnlyToo(t *testing.T) {
	thread := &scriptedThread{
		events:  make(chan uievent.Event, 4),
		history: longThreadHistory(12),
	}
	s := threadScreen(t, stubThreads{"sa-1": thread}, false)

	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)
	if s.thread == nil || !s.thread.hideComposer {
		t.Fatalf("expected a read-only dialog with a hidden composer: present=%v hideComposer=%v",
			s.thread != nil, s.thread != nil && s.thread.hideComposer)
	}

	bottom := s.thread.transcript.Offset()
	if bottom <= 0 {
		t.Fatalf("the test needs an overflowing transcript; got offset %d", bottom)
	}

	for _, k := range []tea.KeyPressMsg{keyMsg("k"), keyMsg("j"), {Code: tea.KeyUp}, {Code: tea.KeyDown}} {
		next, _ = s.Update(k)
		s = next.(Screen)
	}
	if got := s.thread.transcript.Offset(); got != bottom {
		t.Errorf("scroll keys ended at offset %d, want back at %d", got, bottom)
	}
	if s.thread.history.Active() {
		t.Error("up/down opened the prompt-history overlay over a read-only thread dialog")
	}
	if got := s.thread.composer.Value(); got != "" {
		t.Errorf("j/k/arrows reached the hidden thread composer: %q", got)
	}

	for _, ch := range "just" {
		next, _ = s.Update(keyMsg(string(ch)))
		s = next.(Screen)
	}
	if got := s.thread.composer.Value(); got != "" {
		t.Errorf("typing \"just\" reached the hidden thread composer: %q", got)
	}
}

func TestSubagentHistoryDialog_ScrollingAndKeyHandlingWhenComposerHidden(t *testing.T) {
	subCompleted := &scriptedThread{
		events:  make(chan uievent.Event, 4),
		history: longThreadHistory(8),
	}
	threads := stubThreads{"sa-hist": subCompleted}

	s := sized(t, 1)
	next, _ := s.Update(tea.WindowSizeMsg{Width: uikitconfig.BreakpointWide, Height: 30})
	scr := next.(Screen)
	scr.SetSubagentThreads(threads)
	scr.panel.observeAgentHistory("sa-hist", "completed")

	// Open dialog for history subagent
	scr = openPanel(t, scr)
	// Select by what the row IS: the header rows above it move.
	scr.panel.selectNavKind(navAgent, 0) // sa-hist
	next, _ = scr.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	scr = next.(Screen)

	if !scr.thread.hideComposer {
		t.Fatal("expected hideComposer=true for history subagent")
	}

	bottom := scr.thread.transcript.Offset()
	if bottom <= 0 {
		t.Fatalf("the hidden-composer transcript must overflow its viewport; offset=%d", bottom)
	}

	// 1. With the composer hidden, up/down/k/j all scroll exactly one
	// line per press: this dialog is read-only, so none of them may
	// type anywhere.
	scrollPairs := []struct {
		name string
		key  tea.KeyPressMsg
		want int
	}{
		{"k", keyMsg("k"), bottom - 1},
		{"j", keyMsg("j"), bottom},
		{"up", tea.KeyPressMsg{Code: tea.KeyUp}, bottom - 1},
		{"down", tea.KeyPressMsg{Code: tea.KeyDown}, bottom},
	}
	for _, p := range scrollPairs {
		next, _ = scr.Update(p.key)
		scr = next.(Screen)
		if got := scr.thread.transcript.Offset(); got != p.want {
			t.Errorf("%s moved the transcript offset to %d, want %d", p.name, got, p.want)
		}
	}

	// 2. Pgup / pgdown / home / end still run without error and leave
	// the dialog open.
	for _, k := range []tea.KeyPressMsg{
		{Code: tea.KeyPgDown},
		{Code: tea.KeyPgUp},
		{Code: tea.KeyHome},
		{Code: tea.KeyEnd},
	} {
		next, _ = scr.Update(k)
		scr = next.(Screen)
		if scr.thread == nil {
			t.Fatalf("page key %+v closed the thread dialog", k)
		}
	}

	// 3. Typing regular characters should not leak into hidden composer
	next, _ = scr.Update(keyMsg("a"))
	scr = next.(Screen)
	if got := scr.thread.composer.Value(); got != "" {
		t.Errorf("expected thread composer value empty, got %q", got)
	}
	if got := scr.composer.Value(); got != "" {
		t.Errorf("expected main composer value empty, got %q", got)
	}
}
