package conversation

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/keymap"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/replay"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// ctrlEnter is the force-send key: ctrl+enter in the composer.
var ctrlEnter = tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModCtrl}

// TestForceSend_SlashTextOnActiveTurnIsRejected pins the slash-command
// guard: a force-send of a "/..." line must not cancel anything, and the
// composer keeps the text.
func TestForceSend_SlashTextOnActiveTurnIsRejected(t *testing.T) {
	events := []uievent.Event{{Kind: uievent.KindTurnStart, Body: uievent.TurnStartBody{Input: "hi"}}}
	s := newScreen(t, replay.New(events, time.Hour), nil, nil)
	s = typeText(t, s, "hi")
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)
	if s.active == nil {
		t.Fatal("expected active turn")
	}

	s = typeText(t, s, "/model")
	next, _ = s.Update(ctrlEnter)
	got := next.(Screen)

	if got.pendingForce != nil {
		t.Errorf("expected pendingForce nil, got %v", got.pendingForce)
	}
	if got.composer.Value() != "/model" {
		t.Errorf("expected the composer text kept, got %q", got.composer.Value())
	}
	if !strings.Contains(got.statusline.View(fixedNow()), "slash commands cannot be force-sent") {
		t.Errorf("expected the slash-command notice, got %q", got.statusline.View(fixedNow()))
	}
}

// TestForceSend_EmbeddedIsRejected pins the subagent-thread guard: force
// send must never reach an embedded screen's own turn.
func TestForceSend_EmbeddedIsRejected(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s.embedded = true
	handle := &recordingHandle{id: "t1"}
	s.active = handle
	s = typeText(t, s, "interrupt now")

	next, _ := s.Update(ctrlEnter)
	got := next.(Screen)

	if got.pendingForce != nil {
		t.Errorf("expected pendingForce nil in an embedded screen, got %v", got.pendingForce)
	}
	if handle.cancelCount != 0 {
		t.Errorf("expected no Cancel call in an embedded screen, got %d", handle.cancelCount)
	}
	if !strings.Contains(got.statusline.View(fixedNow()), "force send is unavailable in subagent threads") {
		t.Errorf("expected the embedded notice, got %q", got.statusline.View(fixedNow()))
	}
}

// TestForceSend_TextWithActiveTurnForcesAndClears pins the happy path:
// ctrl+enter with text and an active turn parks the text, cancels the
// running turn, and clears the composer.
func TestForceSend_TextWithActiveTurnForcesAndClears(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	handle := &recordingHandle{id: "t1"}
	s.active = handle
	s = typeText(t, s, "force this")

	next, _ := s.Update(ctrlEnter)
	got := next.(Screen)

	if got.pendingForce == nil || *got.pendingForce != "force this" {
		t.Fatalf("expected pendingForce = %q, got %v", "force this", got.pendingForce)
	}
	if handle.cancelCount != 1 {
		t.Errorf("expected Cancel called once, got %d", handle.cancelCount)
	}
	if got.composer.Value() != "" {
		t.Errorf("expected the composer cleared, got %q", got.composer.Value())
	}
}

// TestForceSend_TextWithIdleTurnSendsOrdinarily pins the idle fallback:
// ctrl+enter with no active turn behaves exactly like plain enter.
func TestForceSend_TextWithIdleTurnSendsOrdinarily(t *testing.T) {
	events := []uievent.Event{{Kind: uievent.KindTurnStart, Body: uievent.TurnStartBody{Input: "hi"}}}
	s := newScreen(t, replay.New(events, time.Hour), nil, nil)
	s = typeText(t, s, "hi")

	next, cmd := s.Update(ctrlEnter)
	got := next.(Screen)

	if got.pendingForce != nil {
		t.Errorf("expected pendingForce nil on an idle force-send, got %v", got.pendingForce)
	}
	if got.active == nil {
		t.Fatal("expected an active turn started by the ordinary send path")
	}
	if cmd == nil {
		t.Fatal("expected a Cmd from the ordinary send path")
	}
	if got.composer.Value() != "" {
		t.Errorf("expected the composer cleared by send(), got %q", got.composer.Value())
	}
}

// TestForceSend_EmptyComposerActiveWithQueueForcesTheHead pins the
// no-text path: ctrl+enter on an empty composer with an active turn and
// a non-empty queue force-sends the queue's head.
func TestForceSend_EmptyComposerActiveWithQueueForcesTheHead(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	handle := &recordingHandle{id: "t1"}
	s.active = handle
	s.queue = []string{"A", "B"}

	next, _ := s.Update(ctrlEnter)
	got := next.(Screen)

	if got.pendingForce == nil || *got.pendingForce != "A" {
		t.Fatalf("expected pendingForce = %q, got %v", "A", got.pendingForce)
	}
	if len(got.queue) != 1 || got.queue[0] != "B" {
		t.Fatalf("expected queue = [B], got %v", got.queue)
	}
	if handle.cancelCount != 1 {
		t.Errorf("expected Cancel called once, got %d", handle.cancelCount)
	}
}

// TestForceSend_EmptyComposerIdleIsNoOp pins the quiet no-op: nothing to
// force, nothing to send, nothing changes.
func TestForceSend_EmptyComposerIdleIsNoOp(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)

	next, cmd := s.Update(ctrlEnter)
	got := next.(Screen)

	if got.pendingForce != nil {
		t.Errorf("expected pendingForce nil, got %v", got.pendingForce)
	}
	if got.active != nil {
		t.Errorf("expected no active turn, got %v", got.active)
	}
	if cmd != nil {
		t.Errorf("expected no Cmd, got %+v", cmd())
	}
}

// TestQueueOverlay_ForceOnActiveTurn pins the overlay's own force key:
// with a turn running, f removes the selected item from the queue and
// force-pushes it, closing the overlay.
func TestQueueOverlay_ForceOnActiveTurn(t *testing.T) {
	s := sized(t, 0)
	handle := &recordingHandle{id: "t1"}
	s.active = handle
	s.queue = []string{"item A", "item B"}

	s, _ = press(t, s, tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModCtrl})
	if !s.queueOverlay.Active() {
		t.Fatal("expected queueOverlay active")
	}

	s, _ = press(t, s, key("f"))

	if s.pendingForce == nil || *s.pendingForce != "item A" {
		t.Fatalf("expected pendingForce = %q, got %v", "item A", s.pendingForce)
	}
	if handle.cancelCount != 1 {
		t.Errorf("expected Cancel called once, got %d", handle.cancelCount)
	}
	if len(s.queue) != 1 || s.queue[0] != "item B" {
		t.Fatalf("expected queue = [item B], got %v", s.queue)
	}
	if s.queueOverlay.Active() {
		t.Error("expected the overlay closed after a successful force")
	}
}

// TestQueueOverlay_ForceIdleSendsImmediately pins the overlay's idle
// path: with no active turn, f sends the selected item through
// sendText and closes the overlay with a "sent" notice.
func TestQueueOverlay_ForceIdleSendsImmediately(t *testing.T) {
	s := sized(t, 0)
	s.queue = []string{"item A", "item B"}

	s, _ = press(t, s, tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModCtrl})
	if !s.queueOverlay.Active() {
		t.Fatal("expected queueOverlay active")
	}

	s, cmd := press(t, s, key("f"))

	if s.active == nil {
		t.Fatal("expected the selected item to have started a new turn")
	}
	if cmd == nil {
		t.Fatal("expected a Cmd from the send")
	}
	if len(s.queue) != 1 || s.queue[0] != "item B" {
		t.Fatalf("expected queue = [item B], got %v", s.queue)
	}
	if s.queueOverlay.Active() {
		t.Error("expected the overlay closed after a successful idle send")
	}
	if !strings.Contains(s.statusline.View(fixedNow()), "sent") {
		t.Errorf("expected a \"sent\" notice, got %q", s.statusline.View(fixedNow()))
	}
}

// TestQueueOverlay_ForceFailureRestoresItemAndCursor pins the failure
// path: forcePush's own guard is what can fail here (a queue item is
// possible to enqueue as an all-whitespace string via the composer's
// SetValue-then-force path; forcePush treats it as nothing to send).
// The item is restored at its original index, the cursor lands back on
// it, the overlay stays open, and the notice matches forcePush's own
// message.
func TestQueueOverlay_ForceFailureRestoresItemAndCursor(t *testing.T) {
	s := sized(t, 0)
	handle := &recordingHandle{id: "t1"}
	s.active = handle
	s.queue = []string{"item A", "   ", "item C"}

	s, _ = press(t, s, tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModCtrl})
	if !s.queueOverlay.Active() {
		t.Fatal("expected queueOverlay active")
	}
	// Move to the blank item (index 1).
	s, _ = press(t, s, tea.KeyPressMsg{Code: tea.KeyDown})
	if s.queueOverlay.Cursor() != 1 {
		t.Fatalf("precondition: expected cursor at 1, got %d", s.queueOverlay.Cursor())
	}

	s, _ = press(t, s, key("f"))

	if handle.cancelCount != 0 {
		t.Errorf("expected no Cancel call: forcePush rejected the blank text before touching the turn, got %d", handle.cancelCount)
	}
	if len(s.queue) != 3 || s.queue[0] != "item A" || s.queue[1] != "   " || s.queue[2] != "item C" {
		t.Fatalf("expected queue restored to [item A, \"   \", item C], got %v", s.queue)
	}
	if s.queueOverlay.Cursor() != 1 {
		t.Errorf("expected cursor restored to 1, got %d", s.queueOverlay.Cursor())
	}
	if !s.queueOverlay.Active() {
		t.Error("expected the overlay to stay open after a failed force")
	}
	if !strings.Contains(s.statusline.View(fixedNow()), "nothing to interrupt") {
		t.Errorf("expected the \"nothing to interrupt\" notice, got %q", s.statusline.View(fixedNow()))
	}
}

// TestQueueOverlay_ForceWithEmptyOverlayIsNoOp pins the empty-queue
// guard: f on an empty overlay changes nothing.
func TestQueueOverlay_ForceWithEmptyOverlayIsNoOp(t *testing.T) {
	s := sized(t, 0)
	s.queue = nil

	s, _ = press(t, s, tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModCtrl})
	if !s.queueOverlay.Active() {
		t.Fatal("expected queueOverlay active")
	}

	s, cmd := press(t, s, key("f"))

	if cmd != nil {
		t.Errorf("expected no Cmd, got %+v", cmd())
	}
	if s.pendingForce != nil {
		t.Errorf("expected pendingForce nil, got %v", s.pendingForce)
	}
	if !s.queueOverlay.Active() {
		t.Error("expected the overlay to remain open")
	}
}

// TestApprovalPrecedence_CtrlEnterDoesNotForceSend pins the precedence
// rule at the top of handleModalKey: a pending approval swallows
// ctrl+enter before it ever reaches composerAction's force-send case,
// even with an active turn and composer text that would otherwise
// force-push.
func TestApprovalPrecedence_CtrlEnterDoesNotForceSend(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	handle := &recordingHandle{id: "t1"}
	s.active = handle
	s = typeText(t, s, "force this")
	s.approval.SetRequest(uievent.ToolPendingBody{ToolCallID: "c1", Name: "run_command"})

	next, _ := s.Update(ctrlEnter)
	got := next.(Screen)

	if got.pendingForce != nil {
		t.Errorf("expected pendingForce nil, got %v", got.pendingForce)
	}
	if handle.cancelCount != 0 {
		t.Errorf("expected no Cancel call, got %d", handle.cancelCount)
	}
	if !got.approval.Active() {
		t.Error("expected the approval prompt to remain active")
	}
	if got.composer.Value() != "force this" {
		t.Errorf("expected the composer text kept, got %q", got.composer.Value())
	}
}

// TestApprovalPrecedence_QueueOverlayForceKeyIsSwallowed pins the same
// precedence rule from the other modal: with the queue overlay open AND
// a pending approval, handleApprovalKey (checked first in
// handleModalKey) swallows "f" before handleQueueKey ever sees it.
func TestApprovalPrecedence_QueueOverlayForceKeyIsSwallowed(t *testing.T) {
	s := sized(t, 0)
	handle := &recordingHandle{id: "t1"}
	s.active = handle
	s.queue = []string{"item A", "item B"}

	s, _ = press(t, s, tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModCtrl})
	if !s.queueOverlay.Active() {
		t.Fatal("expected queueOverlay active")
	}
	s.approval.SetRequest(uievent.ToolPendingBody{ToolCallID: "c1", Name: "run_command"})

	s, _ = press(t, s, key("f"))

	if s.pendingForce != nil {
		t.Errorf("expected pendingForce nil, got %v", s.pendingForce)
	}
	if len(s.queue) != 2 || s.queue[0] != "item A" || s.queue[1] != "item B" {
		t.Fatalf("expected queue untouched [item A item B], got %v", s.queue)
	}
	if handle.cancelCount != 0 {
		t.Errorf("expected no Cancel call, got %d", handle.cancelCount)
	}
	if !s.approval.Active() {
		t.Error("expected the approval prompt to remain active")
	}
}

// TestQueueOverlay_ForceIdleSendFailureRestoresItemAndCursor pins the
// overlay's idle-force failure path: sendText's failure restores the
// item at its original index, keeps the cursor on it, leaves the
// overlay open, and notices "send failed; re-queued" - mirroring the
// active-turn failure test above but through the idle branch.
func TestQueueOverlay_ForceIdleSendFailureRestoresItemAndCursor(t *testing.T) {
	conv := &failingConv{id: "idle"}
	s := newScreen(t, conv, nil, nil)
	next, _ := s.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	s = next.(Screen)
	s.queue = []string{"A", "B", "C"}

	s, _ = press(t, s, tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModCtrl})
	if !s.queueOverlay.Active() {
		t.Fatal("expected queueOverlay active")
	}
	s, _ = press(t, s, tea.KeyPressMsg{Code: tea.KeyDown})
	if s.queueOverlay.Cursor() != 1 {
		t.Fatalf("precondition: expected cursor at 1, got %d", s.queueOverlay.Cursor())
	}

	s, _ = press(t, s, key("f"))

	if len(conv.sent) != 1 || conv.sent[0] != "B" {
		t.Fatalf("expected conv.sent = [B], got %v", conv.sent)
	}
	if s.active != nil {
		t.Errorf("expected no active turn after send failure, got %v", s.active)
	}
	if len(s.queue) != 3 || s.queue[0] != "A" || s.queue[1] != "B" || s.queue[2] != "C" {
		t.Fatalf("expected queue restored to [A B C], got %v", s.queue)
	}
	if s.queueOverlay.Cursor() != 1 {
		t.Errorf("expected cursor restored to 1, got %d", s.queueOverlay.Cursor())
	}
	if !s.queueOverlay.Active() {
		t.Error("expected the overlay to stay open after a failed force")
	}
	if !strings.Contains(s.statusline.View(fixedNow()), "send failed; re-queued") {
		t.Errorf("expected the \"send failed; re-queued\" notice, got %q", s.statusline.View(fixedNow()))
	}
}

// TestQueueKeySync_FandUpperF_BothForce pins the table/switch sync: the
// handleQueueKey raw-string switch case ("f", "F") must match exactly
// the ContextDialog IDForceSend row's Keys, so the two cannot drift.
func TestQueueKeySync_FandUpperF_BothForce(t *testing.T) {
	for _, k := range []string{"f", "F"} {
		s := sized(t, 0)
		handle := &recordingHandle{id: "t1"}
		s.active = handle
		s.queue = []string{"only item"}

		s, _ = press(t, s, tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModCtrl})
		if !s.queueOverlay.Active() {
			t.Fatalf("key %q: expected queueOverlay active", k)
		}

		var msg tea.KeyPressMsg
		if k == "F" {
			msg = tea.KeyPressMsg{Code: 'F', Text: "F", Mod: tea.ModShift}
		} else {
			msg = key(k)
		}
		s, _ = press(t, s, msg)

		if s.pendingForce == nil || *s.pendingForce != "only item" {
			t.Errorf("key %q: expected pendingForce = %q, got %v", k, "only item", s.pendingForce)
		}
		if handle.cancelCount != 1 {
			t.Errorf("key %q: expected Cancel called once, got %d", k, handle.cancelCount)
		}
	}

	// The literal binding must deep-equal []string{"f", "F"} so the
	// table and this switch cannot silently diverge.
	m := keymap.New(keymap.Default())
	id, ok := m.Match(keymap.ContextDialog, "f")
	if !ok || id != keymap.IDForceSend {
		t.Fatalf("dialog f = %v/%v, want %v", id, ok, keymap.IDForceSend)
	}
	id, ok = m.Match(keymap.ContextDialog, "F")
	if !ok || id != keymap.IDForceSend {
		t.Fatalf("dialog F = %v/%v, want %v", id, ok, keymap.IDForceSend)
	}
	var found []string
	for _, b := range keymap.Default() {
		if b.ID == keymap.IDForceSend && b.Context == keymap.ContextDialog {
			found = b.Keys
		}
	}
	want := []string{"f", "F"}
	if len(found) != len(want) {
		t.Fatalf("IDForceSend/ContextDialog Keys = %v, want %v", found, want)
	}
	for i := range want {
		if found[i] != want[i] {
			t.Fatalf("IDForceSend/ContextDialog Keys = %v, want %v", found, want)
		}
	}
}

// TestPlainEnterOnActiveTurnStillQueues pins the degraded path: without
// ctrl, Enter on an active turn queues the text exactly as before -
// force-send never claims plain enter.
func TestPlainEnterOnActiveTurnStillQueues(t *testing.T) {
	events := []uievent.Event{{Kind: uievent.KindTurnStart, Body: uievent.TurnStartBody{Input: "hi"}}}
	s := newScreen(t, replay.New(events, time.Hour), nil, nil)
	s = typeText(t, s, "hi")
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)
	if s.active == nil {
		t.Fatal("expected active turn")
	}

	s = typeText(t, s, "queue me")
	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := next.(Screen)

	if got.pendingForce != nil {
		t.Errorf("expected pendingForce nil, got %v", got.pendingForce)
	}
	if len(got.queue) != 1 || got.queue[0] != "queue me" {
		t.Fatalf("expected queue = [queue me], got %v", got.queue)
	}
}

// TestForceSend_WhitespaceOnlyTextOnActiveTurnIsRejected pins the
// TrimSpace guard inside forcePush reached from the composerAction
// force-send case: whitespace-only text is not "empty" (text != "" is
// true), but forcePush rejects it, so composerAction's own success
// branch never runs and the composer keeps its (whitespace) text.
func TestForceSend_WhitespaceOnlyTextOnActiveTurnIsRejected(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	handle := &recordingHandle{id: "t1"}
	s.active = handle
	s = typeText(t, s, "   ")

	next, _ := s.Update(ctrlEnter)
	got := next.(Screen)

	if !strings.Contains(got.statusline.View(fixedNow()), "nothing to interrupt") {
		t.Errorf("expected the \"nothing to interrupt\" notice, got %q", got.statusline.View(fixedNow()))
	}
	if got.pendingForce != nil {
		t.Errorf("expected pendingForce nil, got %v", got.pendingForce)
	}
	if handle.cancelCount != 0 {
		t.Errorf("expected no Cancel call, got %d", handle.cancelCount)
	}
	if got.composer.Value() != "   " {
		t.Errorf("expected the composer to still hold the whitespace text, got %q", got.composer.Value())
	}
}

// TestForceSend_EmptyComposerActiveQueueHeadForcedWithOverlayOpenSwallowsTheKey
// pins the actual production routing: with the queue overlay open,
// ctrl+enter never reaches composerAction's force-send case through
// s.Update at all - handleModalKey's handleQueueKey claims every key
// while the overlay is Active() (its own "f"/"F" case is the queue
// overlay's force-send, a different code path from ctrl+enter, which
// is not one of handleQueueKey's cases and falls to its default,
// swallowed with no effect). This mirrors the overlay-swallow style at
// TestApprovalPrecedence_QueueOverlayForceKeyIsSwallowed above. The
// composer case's own lines (the branch this used to call directly)
// are covered overlay-CLOSED by
// TestForceSend_EmptyComposerActiveWithQueueForcesTheHead.
func TestForceSend_EmptyComposerActiveQueueHeadForcedWithOverlayOpenSwallowsTheKey(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	handle := &recordingHandle{id: "t1"}
	s.active = handle
	s.queue = []string{"A", "B"}

	s, _ = press(t, s, tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModCtrl})
	if !s.queueOverlay.Active() {
		t.Fatal("expected queueOverlay active")
	}

	next, _ := s.Update(ctrlEnter)
	got := next.(Screen)

	if got.pendingForce != nil {
		t.Errorf("expected pendingForce nil: the overlay swallows ctrl+enter, got %v", got.pendingForce)
	}
	if len(got.queue) != 2 || got.queue[0] != "A" || got.queue[1] != "B" {
		t.Fatalf("expected queue untouched [A B], got %v", got.queue)
	}
	if handle.cancelCount != 0 {
		t.Errorf("expected no Cancel call, got %d", handle.cancelCount)
	}
	if !got.queueOverlay.Active() {
		t.Error("expected the queue overlay to remain active")
	}
}

// TestForceSend_EmptyComposerBlankHeadRestoresWithOverlayOpenSwallowsTheKey
// pins the same routing fact from the blank-head setup: with the queue
// overlay open, ctrl+enter is swallowed by handleQueueKey before it can
// reach composerAction's force-send case, so a blank queue head is left
// exactly where it was - nothing to restore, because nothing was ever
// popped. The composer case's own restore-branch lines are covered
// overlay-CLOSED by TestForceSend_EmptyComposerActiveWithQueueForcesTheHead
// (which exercises the same composerAction code, just via a non-blank
// head and with the overlay never opened).
func TestForceSend_EmptyComposerBlankHeadRestoresWithOverlayOpenSwallowsTheKey(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	handle := &recordingHandle{id: "t1"}
	s.active = handle
	s.queue = []string{"   ", "B"}

	s, _ = press(t, s, tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModCtrl})
	if !s.queueOverlay.Active() {
		t.Fatal("expected queueOverlay active")
	}

	next, _ := s.Update(ctrlEnter)
	got := next.(Screen)

	if len(got.queue) != 2 || got.queue[0] != "   " || got.queue[1] != "B" {
		t.Fatalf("expected queue untouched [\"   \", B], got %v", got.queue)
	}
	if !got.queueOverlay.Active() {
		t.Error("expected the queue overlay to remain active")
	}
	if strings.Contains(got.statusline.View(fixedNow()), "nothing to interrupt") {
		t.Errorf("expected no forcePush notice: the key never reached composerAction, got %q", got.statusline.View(fixedNow()))
	}
	if got.pendingForce != nil {
		t.Errorf("expected pendingForce nil, got %v", got.pendingForce)
	}
	if handle.cancelCount != 0 {
		t.Errorf("expected no Cancel call, got %d", handle.cancelCount)
	}
}

// TestQueueDialog_ForceSendRefusedWhileEmbedded pins the defense-in-
// depth guard added to handleQueueKey's own "f"/"F" case: mirroring
// TestForceSend_EmbeddedIsRejected (the composerAction guard this
// duplicates), an embedded screen with the queue overlay open and a
// queue head present must not force-send on "f" - it is latent today
// (IDQueueDialog is swallowed while embedded, so the overlay cannot
// actually open inside a thread), but the case carries its own refusal
// rather than depending on that upstream swallow.
func TestQueueDialog_ForceSendRefusedWhileEmbedded(t *testing.T) {
	s := sized(t, 0)
	s.embedded = true
	handle := &recordingHandle{id: "t1"}
	s.active = handle
	s.queue = []string{"A"}
	s.queueOverlay.Open(s.queue)
	if !s.queueOverlay.Active() {
		t.Fatal("expected queueOverlay active")
	}

	s, _ = press(t, s, key("f"))

	if s.pendingForce != nil {
		t.Errorf("expected pendingForce nil in an embedded screen, got %v", s.pendingForce)
	}
	if handle.cancelCount != 0 {
		t.Errorf("expected no Cancel call in an embedded screen, got %d", handle.cancelCount)
	}
	if len(s.queue) != 1 || s.queue[0] != "A" {
		t.Fatalf("expected queue untouched [A], got %v", s.queue)
	}
	if !s.queueOverlay.Active() {
		t.Error("expected the queue overlay to remain active")
	}
	if !strings.Contains(s.statusline.View(fixedNow()), "force send is unavailable in subagent threads") {
		t.Errorf("expected the embedded notice, got %q", s.statusline.View(fixedNow()))
	}
}
