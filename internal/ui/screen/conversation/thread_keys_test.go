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

// TestSubagentThreadDialogKeys_VisibleEmptyComposerRoutesJKToComposer
// pins the routing rule behind the j/k case split: when the thread
// dialog's composer is live and EMPTY, j/k are typeable keys and must
// reach composer.Value(), while the arrow keys keep scrolling the
// transcript. Before the split, "just rerun tests" went out as
// "ust rerun tests".
func TestSubagentThreadDialogKeys_VisibleEmptyComposerRoutesJKToComposer(t *testing.T) {
	thread := &scriptedThread{
		events:  make(chan uievent.Event, 4),
		history: longThreadHistory(12),
	}
	s := threadScreen(t, stubThreads{"sa-1": thread}, false)

	// Enter on the subagent row opens the live (composer visible) dialog.
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)
	if s.thread == nil || s.thread.hideComposer {
		t.Fatalf("expected a live dialog with a visible composer: present=%v hideComposer=%v",
			s.thread != nil, s.thread != nil && s.thread.hideComposer)
	}

	bottom := s.thread.transcript.Offset()
	if bottom <= 0 {
		t.Fatalf("the test needs an overflowing transcript; got offset %d", bottom)
	}

	// The arrows still scroll a visible but empty composer.
	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	s = next.(Screen)
	if got, want := s.thread.transcript.Offset(), bottom-1; got != want {
		t.Errorf("KeyUp moved the transcript offset to %d, want %d", got, want)
	}
	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	s = next.(Screen)
	if got := s.thread.transcript.Offset(); got != bottom {
		t.Errorf("KeyDown moved the transcript offset to %d, want %d", got, bottom)
	}

	// j/u/s/t type the word; the transcript must not move.
	for _, ch := range "just" {
		next, _ = s.Update(keyMsg(string(ch)))
		s = next.(Screen)
	}
	if got := s.thread.transcript.Offset(); got != bottom {
		t.Errorf("typing \"just\" moved the transcript offset to %d, want %d", got, bottom)
	}
	if got := s.thread.composer.Value(); got != "just" {
		t.Errorf("thread composer holds %q, want \"just\"", got)
	}
}

// TestSubagentThreadDialogKeys_VisibleNonEmptyComposerKeepsAllFourKeys
// pins the other half of the rule: once the visible composer HOLDS
// text, none of up/down/j/k may scroll the transcript at THIS routing
// layer. j/k insert into the composer; the arrows fall through to the
// embedded screen's own handling and never carry text.
func TestSubagentThreadDialogKeys_VisibleNonEmptyComposerKeepsAllFourKeys(t *testing.T) {
	thread := &scriptedThread{
		events:  make(chan uievent.Event, 4),
		history: longThreadHistory(12),
	}
	s := threadScreen(t, stubThreads{"sa-1": thread}, false)

	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)
	if s.thread == nil || s.thread.hideComposer {
		t.Fatalf("expected a live dialog with a visible composer: present=%v hideComposer=%v",
			s.thread != nil, s.thread != nil && s.thread.hideComposer)
	}

	s = typeText(t, s, "hel")
	bottom := s.thread.transcript.Offset()
	if bottom <= 0 {
		t.Fatalf("the test needs an overflowing transcript; got offset %d", bottom)
	}

	next, _ = s.Update(keyMsg("k"))
	s = next.(Screen)
	if got := s.thread.transcript.Offset(); got != bottom {
		t.Errorf("k scrolled the transcript to offset %d, want %d", got, bottom)
	}
	if got := s.thread.composer.Value(); got != "helk" {
		t.Errorf("k did not reach the composer; value is %q, want \"helk\"", got)
	}

	next, _ = s.Update(keyMsg("j"))
	s = next.(Screen)
	if got := s.thread.transcript.Offset(); got != bottom {
		t.Errorf("j scrolled the transcript to offset %d, want %d", got, bottom)
	}
	if got := s.thread.composer.Value(); got != "helkj" {
		t.Errorf("j did not reach the composer; value is %q, want \"helkj\"", got)
	}

	for _, code := range []rune{tea.KeyUp, tea.KeyDown} {
		next, _ = s.Update(tea.KeyPressMsg{Code: code})
		s = next.(Screen)
		if got := s.thread.composer.Value(); got != "helkj" {
			t.Errorf("an arrow key changed the composer text to %q, want \"helkj\"", got)
		}
	}
}

// TestSubagentThreadDialogKeys_VisibleNonEmptyArrowsDoNotOpenHistoryOverlay
// pins the modal half of the arrow rule: inside an open thread dialog,
// up and down with a visible, non-empty composer are caret keys. They
// must not reach the embedded screen's key table, whose prompt-recall
// branch opens the message-history overlay - an overlay must never grow
// over a live modal dialog, and its reserved rows must not shift the
// dialog's transcript layout.
func TestSubagentThreadDialogKeys_VisibleNonEmptyArrowsDoNotOpenHistoryOverlay(t *testing.T) {
	thread := &scriptedThread{
		events:  make(chan uievent.Event, 4),
		history: longThreadHistory(12),
	}
	s := threadScreen(t, stubThreads{"sa-1": thread}, false)

	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)
	if s.thread == nil || s.thread.hideComposer {
		t.Fatalf("expected a live dialog with a visible composer: present=%v hideComposer=%v",
			s.thread != nil, s.thread != nil && s.thread.hideComposer)
	}

	s = typeText(t, s, "hel")
	bottom := s.thread.transcript.Offset()
	if bottom <= 0 {
		t.Fatalf("the test needs an overflowing transcript; got offset %d", bottom)
	}

	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	s = next.(Screen)
	if s.thread.history.Active() {
		t.Error("up opened the prompt-history overlay over the thread dialog")
	}
	if got := s.thread.composer.Value(); got != "hel" {
		t.Errorf("up changed the composer text to %q, want \"hel\"", got)
	}
	if got := s.thread.transcript.Offset(); got != bottom {
		t.Errorf("up shifted the transcript offset to %d, want %d", got, bottom)
	}

	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	s = next.(Screen)
	if s.thread.history.Active() {
		t.Error("down left the prompt-history overlay over the thread dialog")
	}
	if got := s.thread.composer.Value(); got != "hel" {
		t.Errorf("down changed the composer text to %q, want \"hel\"", got)
	}
	if got := s.thread.transcript.Offset(); got != bottom {
		t.Errorf("down shifted the transcript offset to %d, want %d", got, bottom)
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
