// Pin the paste path: tea.PasteMsg from the bracketed-paste protocol must
// reach the composer's textarea so users can paste text from outside the
// app. Before the fix the conversation screen's update() switch had no
// case for tea.PasteMsg and silently dropped the message; the textarea
// has always had the right handler (bubbles/v2 textarea.go line ~1223)
// - the screen just never forwarded the message.
//
// The path the bug took:
//
//	terminal  -> tea.PasteMsg -> app.deliverTop -> Screen.update
//	                                    (no case) -> return s, nil
//
// After the fix the Screen routes the message to its composer and the
// textarea inserts the content at the cursor.
package conversation

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/replay"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// TestPasteMsgInsertsIntoComposer pins the bug fix: a tea.PasteMsg at
// the screen level lands verbatim in the composer, because the only
// surface the user can paste INTO is the composer.
func TestPasteMsgInsertsIntoComposer(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	next, _ := s.Update(tea.PasteMsg{Content: "hello world"})
	scr := next.(Screen)
	if got := scr.composer.Value(); got != "hello world" {
		t.Errorf("got composer %q, want %q after paste", got, "hello world")
	}
}

// TestPastePreservesNewlines pins the multi-line case: a paste that
// carries real newlines must NOT be split into sends. The composer uses
// shift+enter / alt+enter for explicit newlines; bracketed paste is a
// different signal and must land verbatim without firing Enter on any
// embedded LF.
func TestPastePreservesNewlines(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	const multiline = "line1\nline2\nline3"
	next, _ := s.Update(tea.PasteMsg{Content: multiline})
	scr := next.(Screen)
	if got := scr.composer.Value(); got != multiline {
		t.Errorf("got composer %q, want multi-line content preserved verbatim", got)
	}
	if scr.active != nil {
		t.Error("a multi-line paste must not start a turn")
	}
}

// TestPasteReachesExistingDraft pins cursor placement: pasting after the
// user has already typed something appends to the existing draft at the
// cursor, the same way typing would. The textarea's own insertRunes
// handles the byte work; the screen's job is to forward.
func TestPasteReachesExistingDraft(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s = typeText(t, s, "draft:")
	next, _ := s.Update(tea.PasteMsg{Content: "pasted"})
	scr := next.(Screen)
	if got := scr.composer.Value(); got != "draft:pasted" {
		t.Errorf("got composer %q, want paste appended to existing draft", got)
	}
}

// TestPasteWhileTurnActiveIsStillComposerBound pins that a paste during
// an in-flight turn still lands in the composer, never in the transcript.
// The transcript is a renderer of streamed events; the user types into
// the composer regardless of turn state.
func TestPasteWhileTurnActiveIsStillComposerBound(t *testing.T) {
	events := []uievent.Event{{Kind: uievent.KindTurnStart, Body: uievent.TurnStartBody{Input: "x"}}}
	s := newScreen(t, replay.New(events, time.Hour), nil, nil) // long pace: turn stays open
	s = typeText(t, s, "x")
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)
	if s.active == nil {
		t.Fatal("expected an active turn before the paste")
	}

	next, _ = s.Update(tea.PasteMsg{Content: "follow-up"})
	scr := next.(Screen)
	if got := scr.composer.Value(); got != "follow-up" {
		t.Errorf("got composer %q, want the paste to land during an active turn", got)
	}
}
