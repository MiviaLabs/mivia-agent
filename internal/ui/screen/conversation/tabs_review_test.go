package conversation

import (
	"context"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/component/topbar"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

type fakeThreads struct {
	tag string
}

func (f fakeThreads) Thread(string) (ports.Conversation, bool)            { return nil, false }
func (f fakeThreads) CancelSubagentTask(string) (bool, error)             { return false, nil }
func (f fakeThreads) CancelSubagentToolCall(string, string) (bool, error) { return false, nil }

func TestTabReview_EmbeddedMouseNoOp(t *testing.T) {
	s, _, _, runner := setupTwoSessionScreen(t)
	s.embedded = true

	// Register sess-B
	next, _ := s.applyCommandOutcome(runner.SelectSession(context.Background(), "sess-B"))
	s = next.(Screen)
	s.embedded = true

	// Click on row 0 in embedded mode must NOT trigger HitTab
	next, _ = s.Update(leftClick(10, 0))
	if next.(Screen).convID() != "sess-B" {
		t.Errorf("embedded mode must not allow mouse click tab switching, got %q", next.(Screen).convID())
	}
}

func TestTabReview_TabExactIDPriority(t *testing.T) {
	s, _, _, runner := setupTwoSessionScreen(t)
	// sess-A title is "Session A"
	// sess-B title is "Session B"
	// Register sess-B
	next, _ := s.applyCommandOutcome(runner.SelectSession(context.Background(), "sess-B"))
	s = next.(Screen)
	next, _ = s.applyCommandOutcome(runner.SelectSession(context.Background(), "sess-A"))
	s = next.(Screen)

	// Exact ID match on "sess-B"
	next, _ = s.runSlashCommand("/tab sess-B")
	s = next.(Screen)
	if s.convID() != "sess-B" {
		t.Errorf("exact ID match on sess-B failed, got %q", s.convID())
	}

	// Out of bounds numeric index returns error
	next, _ = s.runSlashCommand("/tab 99")
	s = next.(Screen)
	if s.convID() != "sess-B" {
		t.Errorf("out of bounds /tab should remain on sess-B, got %q", s.convID())
	}
}

func TestTabReview_ThreadsPreservedAcrossSwitch(t *testing.T) {
	s, _, _, runner := setupTwoSessionScreen(t)

	// In Session A, attach threads
	s.threads = fakeThreads{tag: "threads-A"}

	// Switch to Session B
	next, _ := s.applyCommandOutcome(runner.SelectSession(context.Background(), "sess-B"))
	sB := next.(Screen)
	if sB.threads != nil {
		t.Fatalf("Session B should not inherit Session A threads, got %v", sB.threads)
	}

	// Switch back to Session A
	next, _ = s.applyCommandOutcome(runner.SelectSession(context.Background(), "sess-A"))
	sA := next.(Screen)
	if ft, ok := sA.threads.(fakeThreads); !ok || ft.tag != "threads-A" {
		t.Errorf("Session A threads should be restored, got %+v", sA.threads)
	}
}

func TestTabReview_TitleSanitizationNewlines(t *testing.T) {
	dark, _, themes := themePair(t)
	convWithNewline := &backgroundTestConversation{
		id:     "sess-nl",
		title:  "Line1\nLine2\rLine3",
		events: make(chan uievent.Event, 10),
	}
	s := New(dark, theme.TierTrueColor, themes, convWithNewline, nil, 80, nil)
	s.topbar.SetTabs([]topbar.SessionTab{
		{ID: "sess-nl", Title: "Line1\nLine2\rLine3", Index: 1, IsCurrent: true},
		{ID: "sess-2", Title: "other", Index: 2},
	})
	view := s.topbar.View()
	// INV-TAB-03: topbar View must be exactly 1 line
	if strings.Contains(view, "\n") {
		t.Errorf("topbar View must not contain newlines, got:\n%s", view)
	}
	if !strings.Contains(ansi.Strip(view), "Line1 Line2") {
		t.Errorf("expected sanitized title with spaces, got:\n%s", ansi.Strip(view))
	}
}
