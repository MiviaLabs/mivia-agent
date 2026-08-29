package conversation

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/intent"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/replay"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// recordingHandle is a ports.TurnHandle that counts Cancel calls, for
// tests asserting forcePush actually cancels the running turn.
type recordingHandle struct {
	id          string
	cancelCount int
}

func (h *recordingHandle) ID() string { return h.id }
func (h *recordingHandle) Events() <-chan uievent.Event {
	ch := make(chan uievent.Event)
	close(ch)
	return ch
}
func (h *recordingHandle) Cancel() { h.cancelCount++ }

func TestForcePush_ParksAndCancels(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	handle := &recordingHandle{id: "t1"}
	s.active = handle

	ok := s.forcePush("do this now")
	if !ok {
		t.Fatal("expected forcePush to report true")
	}
	if s.pendingForce == nil || *s.pendingForce != "do this now" {
		t.Fatalf("expected pendingForce = %q, got %v", "do this now", s.pendingForce)
	}
	if handle.cancelCount != 1 {
		t.Errorf("expected Cancel to be called once, got %d", handle.cancelCount)
	}
	if !strings.Contains(s.statusline.View(fixedNow()), "force-pushed") {
		t.Errorf("expected statusline notice to contain %q, got %q", "force-pushed", s.statusline.View(fixedNow()))
	}
}

func TestForcePush_DisplacesEarlierForceIntoQueueHead(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s.active = &recordingHandle{id: "t1"}
	s.queue = []string{"already queued"}

	if !s.forcePush("A") {
		t.Fatal("expected first forcePush to succeed")
	}
	// active still non-nil for the second forcePush - forcePush does not
	// nil it out itself (that happens async via turnEndedMsg), so a
	// second forcePush before the turn actually ends is a real scenario.
	s.active = &recordingHandle{id: "t2"}

	s.queueOverlay.Open(s.queue)

	if !s.forcePush("B") {
		t.Fatal("expected second forcePush to succeed")
	}
	if s.pendingForce == nil || *s.pendingForce != "B" {
		t.Fatalf("expected pendingForce = %q, got %v", "B", s.pendingForce)
	}
	if len(s.queue) != 2 || s.queue[0] != "A" || s.queue[1] != "already queued" {
		t.Fatalf("expected queue = [A, already queued], got %v", s.queue)
	}
	if got := s.queueOverlay.Items(); len(got) != 2 || got[0] != "A" || got[1] != "already queued" {
		t.Fatalf("expected queueOverlay items resynced to [A, already queued], got %v", got)
	}
	if !s.queueOverlay.Active() {
		t.Error("expected the queue overlay to remain active")
	}
	// forcePush sets a single combined notice when displacing an earlier
	// force: "force-pushed" plus a re-queued preview of the displaced text.
	notice := s.statusline.View(fixedNow())
	if !strings.Contains(notice, "force-pushed; earlier force re-queued: "+preview("A")) {
		t.Errorf("expected combined notice, got %q", notice)
	}
}

func TestForcePush_FalseOnEmptyText(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s.active = &recordingHandle{id: "t1"}

	if s.forcePush("   ") {
		t.Fatal("expected forcePush to report false on blank text")
	}
	if s.pendingForce != nil {
		t.Errorf("expected pendingForce to remain nil, got %v", s.pendingForce)
	}
}

func TestForcePush_FalseOnNilActive(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s.active = nil

	if s.forcePush("hello") {
		t.Fatal("expected forcePush to report false with no active turn")
	}
	if s.pendingForce != nil {
		t.Errorf("expected pendingForce to remain nil, got %v", s.pendingForce)
	}
}

func TestPreview_CollapsesMultilineWhitespace(t *testing.T) {
	got := preview("line one\nline two\r\nline  three")
	want := "line one line two line three"
	if got != want {
		t.Errorf("preview() = %q, want %q", got, want)
	}
}

func TestPreview_TruncatesLongTextWithEllipsis(t *testing.T) {
	long := strings.Repeat("x", 50)
	got := preview(long)
	wantPrefix := strings.Repeat("x", 40)
	if got != wantPrefix+"…" {
		t.Errorf("preview() = %q, want %d x's + ellipsis", got, 40)
	}
	if len([]rune(got)) != 41 {
		t.Errorf("preview() length = %d, want 41", len([]rune(got)))
	}
}

func TestPreview_ShortTextUnchanged(t *testing.T) {
	got := preview("short text")
	if got != "short text" {
		t.Errorf("preview() = %q, want unchanged %q", got, "short text")
	}
}

// TestVisibleDrain_DeliversForceAheadOfQueue pins the visible
// turnEndedMsg branch: a parked force is sent first, the ordinary
// queue order is preserved behind it, and pendingForce is cleared.
func TestVisibleDrain_DeliversForceAheadOfQueue(t *testing.T) {
	events := []uievent.Event{{Kind: uievent.KindTurnStart, Body: uievent.TurnStartBody{Input: "hi"}}}
	s := newScreen(t, replay.New(events, time.Hour), nil, nil)

	s = typeText(t, s, "first")
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)
	if s.active == nil {
		t.Fatal("expected active turn")
	}

	forced := "forced text"
	s.pendingForce = &forced
	s.queue = []string{"queued1", "queued2"}

	next, cmd := s.Update(turnEndedMsg{})
	got := next.(Screen)

	if got.pendingForce != nil {
		t.Errorf("expected pendingForce nil after drain, got %v", got.pendingForce)
	}
	if got.active == nil || cmd == nil {
		t.Fatal("expected the forced text to have started a new turn")
	}
	if len(got.queue) != 2 || got.queue[0] != "queued1" || got.queue[1] != "queued2" {
		t.Errorf("expected queue order preserved [queued1 queued2], got %v", got.queue)
	}
}

// TestVisibleDrain_SendFailureRequeuesForce pins the failure path: the
// forced text lands at queue head (not lost), pendingForce is nil, and
// the notice says so.
func TestVisibleDrain_SendFailureRequeuesForce(t *testing.T) {
	events := []uievent.Event{{Kind: uievent.KindTurnStart, Body: uievent.TurnStartBody{Input: "msg1"}}}
	first := replay.New(events, time.Hour)
	conv := &failOnSecondSendConv{Conversation: first, err: context.DeadlineExceeded}
	s := newScreen(t, conv, nil, nil)

	s = typeText(t, s, "msg1")
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)

	forced := "forced fails"
	s.pendingForce = &forced
	s.queue = []string{"existing"}
	s.queueOverlay.Open(s.queue)

	next, _ = s.Update(turnEndedMsg{})
	got := next.(Screen)

	if got.pendingForce != nil {
		t.Errorf("expected pendingForce nil after failed drain, got %v", got.pendingForce)
	}
	if got.active != nil {
		t.Errorf("expected no active turn after send failure")
	}
	if len(got.queue) != 2 || got.queue[0] != "forced fails" || got.queue[1] != "existing" {
		t.Fatalf("expected queue = [forced fails, existing], got %v", got.queue)
	}
	if items := got.queueOverlay.Items(); len(items) != 2 || items[0] != "forced fails" || items[1] != "existing" {
		t.Fatalf("expected queueOverlay items resynced to [forced fails, existing], got %v", items)
	}
	if !got.queueOverlay.Active() {
		t.Error("expected the queue overlay to remain active")
	}
	notice := got.statusline.View(fixedNow())
	if !strings.Contains(notice, "send failed; re-queued") {
		t.Errorf("expected notice %q, got %q", "send failed; re-queued", notice)
	}
}

// TestBackgroundDrain_DeliversParkedForce pins the background-session
// turnEndedMsg branch: a session's own pendingForce is sent through its
// own conv, sets its own active handle, and other sessions are
// untouched.
func TestBackgroundDrain_DeliversParkedForce(t *testing.T) {
	s, sessA, _, runner := setupTwoSessionScreen(t)

	// Start turn on A, then switch to B so A is a background session.
	s = typeText(t, s, "Task in A")
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)

	next, _ = s.applyCommandOutcome(runner.SelectSession(context.Background(), "sess-B"))
	s = next.(Screen)

	stA := s.sessions["sess-A"]
	if stA == nil {
		t.Fatal("expected sess-A tracked in s.sessions")
	}
	forced := "forced in A"
	stA.pendingForce = &forced

	next, _ = s.Update(turnEndedMsg{sessionID: "sess-A"})
	s = next.(Screen)

	stA = s.sessions["sess-A"]
	if stA.pendingForce != nil {
		t.Errorf("expected sess-A pendingForce nil after drain, got %v", stA.pendingForce)
	}
	if stA.active == nil {
		t.Error("expected sess-A active handle to be set after drain")
	}
	if len(sessA.sent) == 0 || sessA.sent[len(sessA.sent)-1] != "forced in A" {
		t.Errorf("expected sess-A conv to have most recently received %q, got %v", "forced in A", sessA.sent)
	}
	// Session B (foreground) untouched.
	if s.conv.ID() != "sess-B" || s.active != nil {
		t.Errorf("expected session B untouched: id=%q active=%v", s.conv.ID(), s.active)
	}
}

// failingConv always fails Send; used for the background drain failure
// test where the sessionState's own conv - not the sendText path -
// must demote the forced text.
type failingConv struct {
	id      string
	sent    []string
	events  chan uievent.Event
	history []ports.Message
}

func (c *failingConv) Send(_ context.Context, in intent.Send) (ports.TurnHandle, error) {
	c.sent = append(c.sent, in.Text)
	return nil, context.DeadlineExceeded
}
func (c *failingConv) History() []ports.Message             { return c.history }
func (c *failingConv) ActiveTurn() (ports.TurnHandle, bool) { return nil, false }
func (c *failingConv) Model() ports.ModelInfo               { return ports.ModelInfo{Name: c.id} }
func (c *failingConv) ContextUsage() ports.Usage            { return ports.Usage{} }
func (c *failingConv) Title() string                        { return c.id }
func (c *failingConv) ID() string                           { return c.id }

// TestBackgroundDrain_SendFailureRequeuesForce pins the failure path of
// the background branch: the forced text demotes to the session's own
// queue head instead of being dropped.
func TestBackgroundDrain_SendFailureRequeuesForce(t *testing.T) {
	dark, _, themes := themePair(t)
	sessA := &backgroundTestConversation{id: "sess-A", title: "Session A", events: make(chan uievent.Event, 10)}
	sessB := &failingConv{id: "sess-B", events: make(chan uievent.Event, 10)}
	runner := &testMultiSessionRunner{convs: map[string]ports.Conversation{"sess-A": sessA, "sess-B": sessB}}

	s := New(dark, theme.TierASCII, themes, sessA, nil, 80, nil)
	s.SetCommandRunner(runner)
	next, _ := s.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	s = next.(Screen)

	// Start a turn on A, then switch to B (B becomes foreground, A is now
	// the "other" session we can drain in the background).
	s = typeText(t, s, "Task in A")
	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)

	next, _ = s.applyCommandOutcome(runner.SelectSession(context.Background(), "sess-B"))
	s = next.(Screen)

	stA := s.sessions["sess-A"]
	if stA == nil {
		t.Fatal("expected sess-A tracked")
	}
	// Swap sess-A's conv for one whose Send always fails, so its drain
	// exercises the failure branch.
	failingA := &failingConv{id: "sess-A"}
	stA.conv = failingA
	forced := "forced fails in A"
	stA.pendingForce = &forced
	stA.queue = []string{"existing in A"}

	next, _ = s.Update(turnEndedMsg{sessionID: "sess-A"})
	s = next.(Screen)

	stA = s.sessions["sess-A"]
	if stA.pendingForce != nil {
		t.Errorf("expected pendingForce nil, got %v", stA.pendingForce)
	}
	if stA.active != nil {
		t.Errorf("expected no active handle after failed send")
	}
	if len(stA.queue) != 2 || stA.queue[0] != "forced fails in A" || stA.queue[1] != "existing in A" {
		t.Fatalf("expected queue = [forced fails in A, existing in A], got %v", stA.queue)
	}
	// After the failed forced-send demotes the text back to the queue
	// head, the background branch now returns immediately (mirroring the
	// visible branch's drainPendingForce) instead of falling through into
	// the ordinary queue drain. Exactly one Send of the forced text is the
	// correct outcome; a second Send would be a double-send bug.
	if len(failingA.sent) != 1 || failingA.sent[0] != "forced fails in A" {
		t.Errorf("expected failingConv to receive the forced text exactly once, got %v", failingA.sent)
	}

	view := ansi.Strip(stA.transcript.View())
	if !strings.Contains(view, "context deadline exceeded") || !strings.Contains(view, "re-queued") {
		t.Errorf("expected background send failure surfaced in the session transcript, got:\n%s", view)
	}
}

// TestSwitchConversation_PendingForcePlumbing pins the three
// switchConversation states pendingForce must survive: fresh session
// nils it, switch-away saves it, switch-back restores it.
func TestSwitchConversation_PendingForcePlumbing(t *testing.T) {
	s, _, _, runner := setupTwoSessionScreen(t)

	s = typeText(t, s, "Task in A")
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)

	forced := "forced on A"
	s.pendingForce = &forced

	// Switch to fresh session B: pendingForce must be nil.
	next, _ = s.applyCommandOutcome(runner.SelectSession(context.Background(), "sess-B"))
	s = next.(Screen)
	if s.pendingForce != nil {
		t.Errorf("expected fresh session B pendingForce nil, got %v", s.pendingForce)
	}

	// Switch back to A: pendingForce must be restored.
	next, _ = s.applyCommandOutcome(runner.SelectSession(context.Background(), "sess-A"))
	s = next.(Screen)
	if s.pendingForce == nil || *s.pendingForce != "forced on A" {
		t.Fatalf("expected pendingForce restored to %q, got %v", "forced on A", s.pendingForce)
	}
}

// TestForceSendHead_EmptyComposerPopsQueue covers the empty-composer
// IDForceSend path (keys.go's composerAction): with a turn running and
// messages queued, the head pops, parks as the forced message, and the
// queue shrinks. The overlay is deliberately closed: while it is open,
// handleQueueKey swallows every key first (its own "f" row covers the
// dialog force-send).
func TestForceSendHead_EmptyComposerPopsQueue(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	handle := &recordingHandle{id: "t1"}
	s.active = handle
	s.queue = []string{"first", "second"}

	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyF4})
	got := next.(Screen)

	if got.pendingForce == nil || *got.pendingForce != "first" {
		t.Fatalf("expected pendingForce %q, got %v", "first", got.pendingForce)
	}
	if len(got.queue) != 1 || got.queue[0] != "second" {
		t.Fatalf("expected queue [second], got %v", got.queue)
	}
	if handle.cancelCount != 1 {
		t.Errorf("expected Cancel once, got %d", handle.cancelCount)
	}
}

// TestForceSendHead_ResyncsOpenOverlay covers the overlay arm of the
// same helper: with the queue dialog open, popping the head must leave
// the overlay showing exactly what remains.
func TestForceSendHead_ResyncsOpenOverlay(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s.active = &recordingHandle{id: "t1"}
	s.queue = []string{"first", "second"}
	s.queueOverlay.Open(s.queue)

	got := s.forceSendHead()

	if got.pendingForce == nil || *got.pendingForce != "first" {
		t.Fatalf("expected pendingForce %q, got %v", "first", got.pendingForce)
	}
	if items := got.queueOverlay.Items(); len(items) != 1 || items[0] != "second" {
		t.Fatalf("overlay must resync to [second], got %v", items)
	}
}

// TestForceSendHead_RestoresRefusedHead covers the restore arm: the
// force cannot park (the active turn reports refusal through Cancel
// leaving no interruptible state), so the popped head returns to the
// front of the queue unchanged and the notice says nothing was
// interrupted.
func TestForceSendHead_RestoresRefusedHead(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s.active = nil // forcePush refuses: nothing to interrupt
	s.queue = []string{"keep me"}

	got := s.forceSendHead()

	if got.pendingForce != nil {
		t.Fatalf("expected no parked force, got %v", *got.pendingForce)
	}
	if len(got.queue) != 1 || got.queue[0] != "keep me" {
		t.Fatalf("queue head must return unchanged, got %v", got.queue)
	}
	if v := got.statusline.View(fixedNow()); !strings.Contains(v, "nothing to interrupt") {
		t.Fatalf("statusline must say nothing was interrupted, got %q", v)
	}
}

// TestForceSendHead_RestoresWithOverlayOpen covers the restore arm with
// the queue dialog open: the refused head returns to the front and the
// overlay's items are re-synced to match, so the dialog never shows a
// message that is no longer where the queue has it.
func TestForceSendHead_RestoresWithOverlayOpen(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s.active = nil // forcePush refuses: nothing to interrupt
	s.queue = []string{"keep me"}
	s.queueOverlay.Open(s.queue)

	got := s.forceSendHead()

	if got.pendingForce != nil {
		t.Fatalf("expected no parked force, got %v", *got.pendingForce)
	}
	if len(got.queue) != 1 || got.queue[0] != "keep me" {
		t.Fatalf("queue head must return unchanged, got %v", got.queue)
	}
	if items := got.queueOverlay.Items(); len(items) != 1 || items[0] != "keep me" {
		t.Fatalf("overlay must resync after the restore, got %v", items)
	}
}
