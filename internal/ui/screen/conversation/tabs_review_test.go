package conversation

import (
	"context"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/component/topbar"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/keymap"
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

func TestDetachTabOrder(t *testing.T) {
	tests := []struct {
		name, current string
		order, want   []string
		wantNext      string
	}{
		{"middle", "b", []string{"a", "b", "c"}, []string{"a", "c"}, "c"},
		{"last", "c", []string{"a", "b", "c"}, []string{"a", "b"}, "b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, next, ok := detachTabOrder(tt.order, tt.current)
			if !ok || next != tt.wantNext || strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Fatalf("detachTabOrder() = %v, %q, %v; want %v, %q, true", got, next, ok, tt.want, tt.wantNext)
			}
		})
	}
	if _, _, ok := detachTabOrder([]string{"a"}, "a"); ok {
		t.Fatal("single visible tab must not detach")
	}
	if _, _, ok := detachTabOrder([]string{"a", "b"}, "missing"); ok {
		t.Fatal("missing current ID must not detach")
	}
}

func TestDetachCurrentTabPreservesSession(t *testing.T) {
	s, _, _, _ := setupTwoSessionScreen(t)
	s.sessions["sess-A"] = &sessionState{conv: s.conv}
	s.sessionOrder = []string{"sess-A", "sess-B"}
	next, _ := s.detachCurrentTab()
	s = next.(Screen)
	if got := strings.Join(s.sessionOrder, ","); got != "sess-B" {
		t.Fatalf("visible tabs after detach = %q, want sess-B", got)
	}
	if s.sessions["sess-A"] == nil {
		t.Fatal("detached session was removed from session state")
	}
	if s.convID() != "sess-B" {
		t.Fatalf("detaching should select the neighboring session, got %q", s.convID())
	}
}

func TestDetachCurrentTabKeyAndNoOpPaths(t *testing.T) {
	s, _, _, _ := setupTwoSessionScreen(t)
	s.sessions["sess-A"] = &sessionState{conv: s.conv}
	s.sessionOrder = []string{"sess-A", "sess-B"}
	next, _, handled := s.globalAction(keymap.IDTabClose)
	if !handled {
		t.Fatal("F8 tab-close action was not handled")
	}
	if got := next.(Screen).convID(); got != "sess-B" {
		t.Fatalf("F8 dispatch selected %q, want sess-B", got)
	}

	single := s
	single.conv = s.sessions["sess-A"].conv
	single.sessionOrder = []string{"sess-A"}
	if next, cmd := single.detachCurrentTab(); cmd != nil || next.(Screen).sessionOrder[0] != "sess-A" {
		t.Fatal("detaching the only visible tab must be a no-op")
	}
	missing := s
	missing.sessionOrder = []string{"sess-X", "sess-Y"}
	if next, cmd := missing.detachCurrentTab(); cmd != nil || strings.Join(next.(Screen).sessionOrder, ",") != "sess-X,sess-Y" {
		t.Fatal("detaching with an unlisted current session must be a no-op")
	}
}

func TestTabReview_ThreadsPreservedAcrossSwitch(t *testing.T) {
	s, _, _, runner := setupTwoSessionScreen(t)

	// In Session A, attach threads registry and an open thread dialog
	s.threads = fakeThreads{tag: "threads-A"}
	openThreadScreen := s
	s.thread = &openThreadScreen
	s.threadID = "call-1"

	// Switch to Session B
	next, _ := s.applyCommandOutcome(runner.SelectSession(context.Background(), "sess-B"))
	sB := next.(Screen)
	if sB.threads == nil {
		t.Fatalf("Session B should retain shared threads registry, got nil")
	}
	if sB.thread != nil || sB.threadID != "" {
		t.Fatalf("Session B should close active thread dialog from Session A, got thread=%v threadID=%q", sB.thread, sB.threadID)
	}

	// Switch back to Session A
	next, _ = s.applyCommandOutcome(runner.SelectSession(context.Background(), "sess-A"))
	sA := next.(Screen)
	if ft, ok := sA.threads.(fakeThreads); !ok || ft.tag != "threads-A" {
		t.Errorf("Session A threads should be restored, got %+v", sA.threads)
	}
	if sA.thread != nil || sA.threadID != "" {
		t.Errorf("Thread dialog should remain dismissed after switch, got thread=%v threadID=%q", sA.thread, sA.threadID)
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
