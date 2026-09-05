package conversation

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// noticeEvent builds one advisory in the shape SessionPool.pushNotice sends.
func noticeEvent(text string) uievent.Event {
	return uievent.Event{
		Kind: uievent.KindNotice,
		At:   time.Now(),
		Body: uievent.NoticeBody{Text: text},
	}
}

// TestNoticeStreamReachesTheTranscript pins the reader half of ports.Notices.
//
// The port had a producer (SessionPool.Notices) and no consumer anywhere in
// internal/ui, so every out-of-turn advisory - chat-sync lifecycle lines, and
// every workflow progress transition - went into a channel nobody drained.
// Wiring the workflow subscription without this would have moved the silence
// one layer, not ended it.
func TestNoticeStreamReachesTheTranscript(t *testing.T) {
	ch := make(chan uievent.Event, 1)
	s := newTestScreen(t)
	s.SetNotices(ch)

	cmd := s.Init()
	if cmd == nil {
		t.Fatal("Init armed no command with a notice channel set")
	}
	ch <- noticeEvent("workflow wfr-1: step build started")

	next, rearm := s.handleNotice(noticeEvent("workflow wfr-1: step build started"))
	got, ok := next.(Screen)
	if !ok {
		t.Fatalf("handleNotice returned %T, want Screen", next)
	}
	if rearm == nil {
		t.Fatal("handleNotice did not re-arm the reader: the stream stops after one notice")
	}
	if !strings.Contains(renderTranscript(t, got), "step build started") {
		t.Fatal("the notice did not reach the transcript")
	}
}

// TestNoticeReaderSurvivesAnUnreadableBody proves the re-arm is unconditional.
// A body this screen cannot render must be skipped, never treated as
// end-of-stream: dropping the re-arm there would silence every LATER notice
// because of one malformed earlier one.
func TestNoticeReaderSurvivesAnUnreadableBody(t *testing.T) {
	s := newTestScreen(t)
	s.SetNotices(make(chan uievent.Event, 1))

	_, rearm := s.handleNotice(uievent.Event{Kind: uievent.KindNotice, Body: uievent.TextEndBody{Text: "wrong shape"}})
	if rearm == nil {
		t.Fatal("an unreadable notice body stopped the reader")
	}
}

// TestNoticeReaderDisabledWithoutAChannel pins the nil case: every embedded
// thread Screen and every Screen a test builds without wiring one must arm no
// goroutine at all.
func TestNoticeReaderDisabledWithoutAChannel(t *testing.T) {
	s := newTestScreen(t)
	if cmd := s.awaitNotice(); cmd != nil {
		t.Fatal("awaitNotice armed a reader with no channel set")
	}
}

// newTestScreen builds a Screen with no conversation wiring: nothing here
// sends a turn, so the transcript and the notice reader are all that matter.
func newTestScreen(t *testing.T) Screen {
	t.Helper()
	return newScreen(t, errConversation{}, nil, nil)
}

// renderTranscript draws the screen and returns its text.
func renderTranscript(t *testing.T, s Screen) string {
	t.Helper()
	next, _ := s.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	sized, ok := next.(Screen)
	if !ok {
		t.Fatalf("Update returned %T, want Screen", next)
	}
	return sized.View()
}
