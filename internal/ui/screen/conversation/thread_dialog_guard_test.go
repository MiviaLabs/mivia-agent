// thread_dialog_guard_test.go isolates the remaining unproven operands of
// threadDialogKey's exit-key guard and threadDialogScrollKey's
// composerReady/hidden booleans (thread.go). esc and ctrl+c already close
// the dialog under dedicated tests (TestSubagentThreadEscClosesWithoutLeaking,
// TestSubagentThreadCtrlCClosesDialogWithoutQuitting in thread_test.go);
// ctrl+b did not have one of its own, leaving that `==` unproven. The
// composerReady/hidden booleans are computed unconditionally on every call
// (nothing to do with the key pressed), so tests target their operands
// directly rather than through a specific case.
package conversation

import (
	tea "charm.land/bubbletea/v2"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// TestThreadDialogKey_CtrlBClosesDialog proves ctrl+b alone closes the
// open thread dialog, isolating the `msg.String() == "ctrl+b"` leg of
// threadDialogKey's three-way exit-key OR from esc and ctrl+c (each
// already proven by their own dedicated tests elsewhere in this
// package). A mutation of this specific `==` to `!=` would make ctrl+b
// leave the dialog open (the other two literal comparisons are both
// false for this key), which this test catches.
func TestThreadDialogKey_CtrlBClosesDialog(t *testing.T) {
	s := threadScreen(t, stubThreads{"sa-1": &scriptedThread{events: make(chan uievent.Event, 4)}}, false)
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)
	if !s.panel.dialog || s.panel.dialogAgent != "sa-1" {
		t.Fatal("enter did not open the thread dialog")
	}

	next, _ = s.Update(tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
	s = next.(Screen)

	if s.panel.dialog || s.panel.dialogAgent != "" {
		t.Fatal("ctrl+b did not close the thread dialog")
	}
}

// TestThreadDialogKey_NilThreadIsSafeNoop proves threadDialogKey's own
// `s.thread == nil` guard (thread.go, just after the exit-key check and
// the threadDialogScrollKey delegation) returns cleanly instead of falling
// through to `s.thread.hideComposer`, which would nil-dereference. A
// mutation of this `==` to `!=` would invert the guard: it would return
// early only when a thread IS present, and fall through to the nil
// dereference when it is not - panicking on the very call this test
// makes.
func TestThreadDialogKey_NilThreadIsSafeNoop(t *testing.T) {
	var s Screen // s.thread == nil; not an exit key, not a scroll key.

	next, cmd := s.threadDialogKey(tea.KeyPressMsg{Text: "z", Code: 'z'}) // must not panic
	if cmd != nil {
		t.Fatalf("threadDialogKey returned a non-nil Cmd for a nil-thread no-op: %v", cmd)
	}
	if _, ok := next.(Screen); !ok {
		t.Fatalf("threadDialogKey returned a non-Screen app.Screen: %T", next)
	}
}

// TestThreadDialogScrollKey_NilThreadIsSafeNoop proves threadDialogScrollKey
// does not panic when s.thread is nil, and reports handled=false. Both
// composerReady (`s.thread != nil && (...)`) and hidden (`s.thread != nil
// && s.thread.hideComposer`) are computed unconditionally at the top of
// the function on every call, so this single case isolates BOTH `&&`
// mutations (thread.go:286 outer, thread.go:287): with s.thread nil, the
// left operand of each `&&` is false, and the ORIGINAL code short-circuits
// without evaluating the right operand. A mutant that turns either `&&`
// into `||` forces evaluation of the right operand too (Go's || only
// skips the right side when the LEFT is true), dereferencing the nil
// s.thread and panicking.
func TestThreadDialogScrollKey_NilThreadIsSafeNoop(t *testing.T) {
	var s Screen // s.thread == nil

	_, _, handled := s.threadDialogScrollKey(tea.KeyPressMsg{Code: tea.KeyHome}) // must not panic
	if handled {
		t.Fatal("threadDialogScrollKey reported handled=true with s.thread == nil")
	}
}

// TestThreadDialogScrollKey_ComposerReadyTrueViaEmptyValueAlone isolates
// composerReady's inner `||` (thread.go:286, `hideComposer ||
// composer.Value() == ""`) and its `==` operand together: with
// hideComposer forced FALSE, composerReady can only be true because the
// composer's value is empty. A mutant that turns the inner `||` into `&&`
// would make composerReady false here (false && true == false, differing
// from the original's false || true == true); a mutant that turns the
// `==` into `!=` would also make composerReady false here (the empty
// value would fail the flipped check). Either way "home" would not
// scroll, which this test's offset assertion catches.
func TestThreadDialogScrollKey_ComposerReadyTrueViaEmptyValueAlone(t *testing.T) {
	s := threadScreen(t, stubThreads{"sa-1": &scriptedThread{
		events:  make(chan uievent.Event, 4),
		history: longThreadHistory(12),
	}}, false)
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)
	if s.thread == nil {
		t.Fatal("setup: thread dialog did not open")
	}
	s.thread.hideComposer = false // forces hideComposer leg false
	if got := s.thread.composer.Value(); got != "" {
		t.Fatalf("setup: expected an empty thread composer, got %q", got)
	}

	bottom := s.thread.transcript.Offset()
	if bottom <= 0 {
		t.Fatalf("the test needs an overflowing transcript; got offset %d", bottom)
	}

	next2, _, handled := s.threadDialogScrollKey(tea.KeyPressMsg{Code: tea.KeyHome})
	if !handled {
		t.Fatal("threadDialogScrollKey reported handled=false for \"home\" with composerReady true via the empty-value leg")
	}
	scr, ok := next2.(Screen)
	if !ok {
		t.Fatalf("threadDialogScrollKey returned a non-Screen app.Screen: %T", next2)
	}
	if got := scr.thread.transcript.Offset(); got != 0 {
		t.Fatalf("\"home\" left the transcript offset at %d, want 0 (ScrollToTop)", got)
	}
}

// TestThreadDialogScrollKey_HiddenFalseWhenComposerVisible isolates
// hidden's `&&` (thread.go:287, `s.thread != nil && s.thread.hideComposer`)
// with an explicit behavioral assertion (rather than relying on the nil-
// thread panic in the test above): with a non-nil thread and hideComposer
// forced false, hidden must be false, so "tab" must NOT move transcript
// focus. A mutant turning this `&&` into `||` would make hidden true
// regardless of hideComposer (since s.thread != nil is already true),
// wrongly focusing the transcript.
func TestThreadDialogScrollKey_HiddenFalseWhenComposerVisible(t *testing.T) {
	s := threadScreen(t, stubThreads{"sa-1": &scriptedThread{
		events:  make(chan uievent.Event, 4),
		history: longThreadHistory(12),
	}}, false)
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)
	if s.thread == nil {
		t.Fatal("setup: thread dialog did not open")
	}
	s.thread.hideComposer = false
	if s.thread.transcript.Focused() {
		t.Fatal("setup: the thread transcript must start unfocused")
	}

	next2, _, handled := s.threadDialogScrollKey(tea.KeyPressMsg{Text: "tab", Code: tea.KeyTab})
	if handled {
		t.Fatal("threadDialogScrollKey reported handled=true for \"tab\" with hidden false")
	}
	scr, ok := next2.(Screen)
	if !ok {
		t.Fatalf("threadDialogScrollKey returned a non-Screen app.Screen: %T", next2)
	}
	if scr.thread.transcript.Focused() {
		t.Fatal("\"tab\" focused the transcript despite hidden == false")
	}
}
