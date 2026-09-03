// remote_cancel_target_test.go covers the remote targeted-cancel path: the
// body parser, the session/seam resolver, and the routing of "cancel_task"
// and "cancel_tool_call" onto the three local cancel seams.
package conversation

import (
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// recordingThreads records the exact arguments each subagent cancel seam
// was called with, so a test can assert the ARGUMENT ORDER rather than only
// that a call happened - swapping (rowID, toolCallID) is the obvious bug on
// this path and produces a silent miss in production.
type recordingThreads struct {
	stubThreads

	mu       sync.Mutex
	taskIDs  []string
	toolArgs [][2]string
	ok       bool
}

func (r *recordingThreads) CancelSubagentTask(callID string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.taskIDs = append(r.taskIDs, callID)
	return r.ok, nil
}

func (r *recordingThreads) CancelSubagentToolCall(callID, toolCallID string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.toolArgs = append(r.toolArgs, [2]string{callID, toolCallID})
	return r.ok, nil
}

func (r *recordingThreads) tasks() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.taskIDs...)
}

func (r *recordingThreads) tools() [][2]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][2]string(nil), r.toolArgs...)
}

// toolCancelTrackingHandle records which MAIN-turn tool call ids
// CancelToolCall was asked for, and reports the hit it is configured with.
type toolCancelTrackingHandle struct {
	cancelTrackingTurnHandle
	asked []string
	hit   bool
}

func (h *toolCancelTrackingHandle) CancelToolCall(callID string) bool {
	h.asked = append(h.asked, callID)
	return h.hit
}

// remoteCancelScreen builds a foreground screen for session id with the
// given subagent seam wired.
func remoteCancelScreen(t *testing.T, id string, threads ports.SubagentThreads) Screen {
	t.Helper()
	dark, _, themes := themePair(t)
	s := New(dark, theme.TierASCII, themes, &oversizedTurnConversation{id: id}, nil, 80, nil)
	s.threads = threads
	return s
}

// runCmd runs a tea.Cmd off the update goroutine and returns its message,
// failing if it does not produce one within the budget.
func runCmd(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a tea.Cmd, got nil")
	}
	out := make(chan tea.Msg, 1)
	go func() { out <- cmd() }()
	select {
	case msg := <-out:
		return msg
	case <-time.After(2 * time.Second):
		t.Fatal("the Cmd never produced a message")
		return nil
	}
}

// --- body parser -----------------------------------------------------

func TestParseRemoteCancelTarget(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantID  string
		wantTCI string
		wantOK  bool
	}{
		{name: "one part", body: "call-1:task-a", wantID: "call-1:task-a", wantOK: true},
		{name: "two parts", body: "call-1:task-a tc-9", wantID: "call-1:task-a", wantTCI: "tc-9", wantOK: true},
		{name: "surrounding whitespace", body: "  call-1:task-a   tc-9 \t", wantID: "call-1:task-a", wantTCI: "tc-9", wantOK: true},
		{name: "one part padded", body: "   tc-9   ", wantID: "tc-9", wantOK: true},
		{name: "empty", body: "", wantOK: false},
		{name: "whitespace only", body: "   \t ", wantOK: false},
		{name: "three parts", body: "a b c", wantOK: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseRemoteCancelTarget(tc.body)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (target %+v)", ok, tc.wantOK, got)
			}
			if !tc.wantOK {
				return
			}
			if got.id != tc.wantID || got.toolCallID != tc.wantTCI {
				t.Errorf("target = %+v, want id %q toolCallID %q", got, tc.wantID, tc.wantTCI)
			}
		})
	}
}

// --- cancel_task routing ---------------------------------------------

// TestRemoteCancelTaskReachesSeam proves a "cancel_task" remote input calls
// CancelSubagentTask with exactly the row id the body carried.
func TestRemoteCancelTaskReachesSeam(t *testing.T) {
	threads := &recordingThreads{stubThreads: stubThreads{}, ok: true}
	s := remoteCancelScreen(t, "sess-1", threads)

	_, cmd := s.handleRemoteTargetedCancel(ports.RemoteInputEvent{
		SessionID: "sess-1", Kind: "cancel_task", Body: " call-1:task-a ",
	})
	msg := runCmd(t, cmd)
	res, ok := msg.(subagentTaskCancelResultMsg)
	if !ok {
		t.Fatalf("Cmd reported %T, want subagentTaskCancelResultMsg", msg)
	}
	if !res.ok {
		t.Errorf("result = %+v, want a hit", res)
	}
	if got := threads.tasks(); len(got) != 1 || got[0] != "call-1:task-a" {
		t.Fatalf("CancelSubagentTask called with %v, want [call-1:task-a]", got)
	}
	if len(threads.tools()) != 0 {
		t.Errorf("cancel_task wrongly reached the tool-call seam: %v", threads.tools())
	}
}

// TestRemoteCancelTaskRefusesTwoPartBody isolates the
// `target.toolCallID != ""` guard in remoteCancelSubagentTask: a
// "cancel_task" naming two ids is ambiguous and must not be cancelled as if
// it named one.
func TestRemoteCancelTaskRefusesTwoPartBody(t *testing.T) {
	threads := &recordingThreads{stubThreads: stubThreads{}, ok: true}
	s := remoteCancelScreen(t, "sess-1", threads)

	next, cmd := s.handleRemoteTargetedCancel(ports.RemoteInputEvent{
		SessionID: "sess-1", Kind: "cancel_task", Body: "call-1:task-a tc-9",
	})
	if cmd != nil {
		t.Fatal("a two-part cancel_task returned a Cmd; it must be refused")
	}
	if got := threads.tasks(); len(got) != 0 {
		t.Fatalf("CancelSubagentTask was called with %v, want no call", got)
	}
	assertNotice(t, next, "must name exactly one task")
}

// TestRemoteTargetedCancelRefusesMalformedBody isolates the parser refusal
// leg of handleRemoteTargetedCancel: three parts reaches no seam at all.
func TestRemoteTargetedCancelRefusesMalformedBody(t *testing.T) {
	threads := &recordingThreads{stubThreads: stubThreads{}, ok: true}
	s := remoteCancelScreen(t, "sess-1", threads)

	next, cmd := s.handleRemoteTargetedCancel(ports.RemoteInputEvent{
		SessionID: "sess-1", Kind: "cancel_tool_call", Body: "a b c",
	})
	if cmd != nil {
		t.Fatal("a malformed body returned a Cmd; it must be refused")
	}
	if len(threads.tasks()) != 0 || len(threads.tools()) != 0 {
		t.Fatal("a malformed body reached a cancel seam")
	}
	assertNotice(t, next, "malformed target")
}

// TestRemoteCancelTaskNoThreadsSeamIsNoOp isolates the `seams.threads ==
// nil` guard: a screen with no SubagentThreads wired must be a silent
// no-op, never a nil dereference.
func TestRemoteCancelTaskNoThreadsSeamIsNoOp(t *testing.T) {
	s := remoteCancelScreen(t, "sess-1", nil)
	next, cmd := s.handleRemoteTargetedCancel(ports.RemoteInputEvent{
		SessionID: "sess-1", Kind: "cancel_task", Body: "call-1:task-a",
	})
	if cmd != nil {
		t.Fatal("cancel_task with no threads seam returned a Cmd")
	}
	if _, ok := next.(Screen); !ok {
		t.Fatalf("handler returned %T, want Screen", next)
	}
}

// --- cancel_tool_call routing ----------------------------------------

// TestRemoteCancelToolCallOnePartReachesMainTurn proves a one-part
// "cancel_tool_call" goes to the MAIN turn handle, not the subagent seam.
func TestRemoteCancelToolCallOnePartReachesMainTurn(t *testing.T) {
	threads := &recordingThreads{stubThreads: stubThreads{}, ok: true}
	s := remoteCancelScreen(t, "sess-1", threads)
	handle := &toolCancelTrackingHandle{
		cancelTrackingTurnHandle: cancelTrackingTurnHandle{id: "turn-1", events: make(chan uievent.Event)},
		hit:                      true,
	}
	s.active = handle

	next, cmd := s.handleRemoteTargetedCancel(ports.RemoteInputEvent{
		SessionID: "sess-1", Kind: "cancel_tool_call", Body: "tc-9",
	})
	if cmd != nil {
		t.Fatal("the main-turn leg returned a Cmd; it cancels in-process")
	}
	if len(handle.asked) != 1 || handle.asked[0] != "tc-9" {
		t.Fatalf("CancelToolCall asked for %v, want [tc-9]", handle.asked)
	}
	if len(threads.tools()) != 0 {
		t.Errorf("a one-part cancel_tool_call wrongly reached the subagent seam: %v", threads.tools())
	}
	assertNotice(t, next, "cancelling tc-9")
}

// TestRemoteCancelToolCallTwoPartsArgumentOrder is the argument-order pin:
// CancelSubagentToolCall must be called (row id, tool call id) in THAT
// order. Swapping them still compiles and still returns cleanly, so only an
// explicit assertion catches it.
func TestRemoteCancelToolCallTwoPartsArgumentOrder(t *testing.T) {
	threads := &recordingThreads{stubThreads: stubThreads{}, ok: true}
	s := remoteCancelScreen(t, "sess-1", threads)
	handle := &toolCancelTrackingHandle{
		cancelTrackingTurnHandle: cancelTrackingTurnHandle{id: "turn-1", events: make(chan uievent.Event)},
		hit:                      true,
	}
	s.active = handle

	_, cmd := s.handleRemoteTargetedCancel(ports.RemoteInputEvent{
		SessionID: "sess-1", Kind: "cancel_tool_call", Body: "call-1:task-a tc-9",
	})
	msg := runCmd(t, cmd)
	res, ok := msg.(threadToolCallCancelResultMsg)
	if !ok {
		t.Fatalf("Cmd reported %T, want threadToolCallCancelResultMsg", msg)
	}
	if !res.ok {
		t.Errorf("result = %+v, want a hit", res)
	}
	got := threads.tools()
	if len(got) != 1 {
		t.Fatalf("CancelSubagentToolCall calls = %v, want exactly one", got)
	}
	if got[0][0] != "call-1:task-a" {
		t.Errorf("first argument = %q, want the SUBAGENT ROW id %q (arguments are swapped)", got[0][0], "call-1:task-a")
	}
	if got[0][1] != "tc-9" {
		t.Errorf("second argument = %q, want the TOOL CALL id %q (arguments are swapped)", got[0][1], "tc-9")
	}
	if len(handle.asked) != 0 {
		t.Errorf("a two-part cancel_tool_call wrongly reached the main turn: %v", handle.asked)
	}
}

// TestRemoteCancelToolCallNoActiveTurnIsNoOp isolates the `seams.active ==
// nil` guard on the main-turn leg: the turn may simply have finished before
// the instruction arrived, which is a miss, not an error.
func TestRemoteCancelToolCallNoActiveTurnIsNoOp(t *testing.T) {
	s := remoteCancelScreen(t, "sess-1", &recordingThreads{stubThreads: stubThreads{}, ok: true})
	next, cmd := s.handleRemoteTargetedCancel(ports.RemoteInputEvent{
		SessionID: "sess-1", Kind: "cancel_tool_call", Body: "tc-9",
	})
	if cmd != nil {
		t.Fatal("a cancel_tool_call with no active turn returned a Cmd")
	}
	if scr := next.(Screen); scr.statusline.Active() {
		t.Errorf("a miss emitted a notice: %q", scr.statusline.View(time.Now()))
	}
}

// TestRemoteCancelToolCallMissEmitsNoNotice isolates the
// `seams.active.CancelToolCall(...)` result guard: a false return (the call
// already finished, or the id names nothing in flight) must stay quiet.
func TestRemoteCancelToolCallMissEmitsNoNotice(t *testing.T) {
	s := remoteCancelScreen(t, "sess-1", nil)
	handle := &toolCancelTrackingHandle{
		cancelTrackingTurnHandle: cancelTrackingTurnHandle{id: "turn-1", events: make(chan uievent.Event)},
		hit:                      false,
	}
	s.active = handle

	next, _ := s.handleRemoteTargetedCancel(ports.RemoteInputEvent{
		SessionID: "sess-1", Kind: "cancel_tool_call", Body: "tc-gone",
	})
	if len(handle.asked) != 1 {
		t.Fatalf("CancelToolCall asked for %v, want exactly one attempt", handle.asked)
	}
	if scr := next.(Screen); scr.statusline.Active() {
		t.Errorf("a missed cancel emitted a notice: %q", scr.statusline.View(time.Now()))
	}
}

// TestRemoteCancelToolCallTwoPartsNoThreadsSeamIsNoOp isolates the
// `seams.threads == nil` guard on the subagent leg.
func TestRemoteCancelToolCallTwoPartsNoThreadsSeamIsNoOp(t *testing.T) {
	s := remoteCancelScreen(t, "sess-1", nil)
	_, cmd := s.handleRemoteTargetedCancel(ports.RemoteInputEvent{
		SessionID: "sess-1", Kind: "cancel_tool_call", Body: "call-1:task-a tc-9",
	})
	if cmd != nil {
		t.Fatal("a two-part cancel_tool_call with no threads seam returned a Cmd")
	}
}

// --- session resolution ----------------------------------------------

// TestRemoteTargetedCancelEmptySessionIDTargetsForeground isolates the
// `sessionID == ""` operand of resolveRemoteCancelSeams.
func TestRemoteTargetedCancelEmptySessionIDTargetsForeground(t *testing.T) {
	threads := &recordingThreads{stubThreads: stubThreads{}, ok: true}
	s := remoteCancelScreen(t, "sess-fg", threads)

	_, cmd := s.handleRemoteTargetedCancel(ports.RemoteInputEvent{
		SessionID: "", Kind: "cancel_task", Body: "call-1:task-a",
	})
	runCmd(t, cmd)
	if got := threads.tasks(); len(got) != 1 {
		t.Fatalf("an empty session id did not reach the foreground seam: %v", got)
	}
}

// TestRemoteTargetedCancelMatchingSessionIDTargetsForeground isolates the
// `sessionID == s.convID()` operand of the same guard.
func TestRemoteTargetedCancelMatchingSessionIDTargetsForeground(t *testing.T) {
	threads := &recordingThreads{stubThreads: stubThreads{}, ok: true}
	s := remoteCancelScreen(t, "sess-fg", threads)

	_, cmd := s.handleRemoteTargetedCancel(ports.RemoteInputEvent{
		SessionID: "sess-fg", Kind: "cancel_task", Body: "call-1:task-a",
	})
	runCmd(t, cmd)
	if got := threads.tasks(); len(got) != 1 {
		t.Fatalf("a matching session id did not reach the foreground seam: %v", got)
	}
}

// TestRemoteTargetedCancelTargetsBackgroundSession proves a targeted cancel
// aimed at a tracked BACKGROUND session reaches that session's own seam,
// not the foreground one - the same fork handleRemoteCancel applies.
func TestRemoteTargetedCancelTargetsBackgroundSession(t *testing.T) {
	fg := &recordingThreads{stubThreads: stubThreads{}, ok: true}
	bg := &recordingThreads{stubThreads: stubThreads{}, ok: true}
	s := remoteCancelScreen(t, "sess-fg", fg)
	s.sessions["sess-bg"] = &sessionState{threads: bg}

	_, cmd := s.handleRemoteTargetedCancel(ports.RemoteInputEvent{
		SessionID: "sess-bg", Kind: "cancel_task", Body: "call-1:task-a",
	})
	runCmd(t, cmd)
	if got := bg.tasks(); len(got) != 1 || got[0] != "call-1:task-a" {
		t.Fatalf("the background session's seam was called with %v, want [call-1:task-a]", got)
	}
	if got := fg.tasks(); len(got) != 0 {
		t.Errorf("the foreground seam was wrongly called with %v", got)
	}
}

// TestRemoteTargetedCancelUntrackedSessionIsNoOp isolates the `!ok` map
// miss in resolveRemoteCancelSeams: a session this screen never tracked
// must not fall through onto the foreground session's seams.
func TestRemoteTargetedCancelUntrackedSessionIsNoOp(t *testing.T) {
	fg := &recordingThreads{stubThreads: stubThreads{}, ok: true}
	s := remoteCancelScreen(t, "sess-fg", fg)

	_, cmd := s.handleRemoteTargetedCancel(ports.RemoteInputEvent{
		SessionID: "sess-never-tracked", Kind: "cancel_task", Body: "call-1:task-a",
	})
	if cmd != nil {
		t.Fatal("an untracked session returned a Cmd")
	}
	if got := fg.tasks(); len(got) != 0 {
		t.Fatalf("an untracked session's cancel leaked onto the foreground seam: %v", got)
	}
}

// --- dispatch from handleRemoteInput ---------------------------------

// TestHandleRemoteInputDispatchesCancelTask proves handleRemoteInput itself
// routes the kind, rather than falling through to the send path (which
// would inject the row id into the transcript as a user message).
func TestHandleRemoteInputDispatchesCancelTask(t *testing.T) {
	threads := &recordingThreads{stubThreads: stubThreads{}, ok: true}
	conv := &oversizedTurnConversation{id: "sess-1"}
	dark, _, themes := themePair(t)
	s := New(dark, theme.TierASCII, themes, conv, nil, 80, nil)
	s.threads = threads

	_, cmd := s.handleRemoteInput(ports.RemoteInputEvent{
		SessionID: "sess-1", Kind: "cancel_task", Body: "call-1:task-a",
	})
	runCmd(t, cmd)
	if got := threads.tasks(); len(got) != 1 || got[0] != "call-1:task-a" {
		t.Fatalf("handleRemoteInput did not route cancel_task to the seam: %v", got)
	}
	if len(conv.sent) != 0 {
		t.Errorf("cancel_task was sent as a message: %v", conv.sent)
	}
}

// TestHandleRemoteInputDispatchesCancelToolCall isolates the other operand
// of handleRemoteInput's `ev.Kind == "cancel_task" || ev.Kind ==
// "cancel_tool_call"` dispatch guard.
func TestHandleRemoteInputDispatchesCancelToolCall(t *testing.T) {
	threads := &recordingThreads{stubThreads: stubThreads{}, ok: true}
	conv := &oversizedTurnConversation{id: "sess-1"}
	dark, _, themes := themePair(t)
	s := New(dark, theme.TierASCII, themes, conv, nil, 80, nil)
	s.threads = threads

	_, cmd := s.handleRemoteInput(ports.RemoteInputEvent{
		SessionID: "sess-1", Kind: "cancel_tool_call", Body: "call-1:task-a tc-9",
	})
	runCmd(t, cmd)
	if got := threads.tools(); len(got) != 1 || got[0] != [2]string{"call-1:task-a", "tc-9"} {
		t.Fatalf("handleRemoteInput did not route cancel_tool_call to the seam: %v", got)
	}
	if len(conv.sent) != 0 {
		t.Errorf("cancel_tool_call was sent as a message: %v", conv.sent)
	}
}

// TestHandleRemoteInputMessageKindStillSends is the negative half of that
// dispatch guard: an ordinary "message" must still reach the send path and
// must NOT be treated as a cancel.
func TestHandleRemoteInputMessageKindStillSends(t *testing.T) {
	threads := &recordingThreads{stubThreads: stubThreads{}, ok: true}
	conv := &oversizedTurnConversation{id: "sess-1"}
	dark, _, themes := themePair(t)
	s := New(dark, theme.TierASCII, themes, conv, nil, 80, nil)
	s.threads = threads

	_, _ = s.handleRemoteInput(ports.RemoteInputEvent{
		SessionID: "sess-1", Kind: "message", Body: "hello",
	})
	if len(conv.sent) != 1 || conv.sent[0] != "hello" {
		t.Fatalf("a message input was not sent: %v", conv.sent)
	}
	if len(threads.tasks()) != 0 || len(threads.tools()) != 0 {
		t.Error("a message input reached a cancel seam")
	}
}
