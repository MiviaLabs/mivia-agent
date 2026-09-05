package conversation

import (
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// TestUpdateRoutesEveryAsyncPortMsgThroughTheSwitch covers
// updateAsyncPortMsg's own case-dispatch lines for noticeMsg,
// workflowStatusMsg, sessionMountedMsg, subagentTaskCancelResultMsg, and
// threadToolCallCancelResultMsg. The handlers behind each case already have
// direct, thorough tests elsewhere in this package; this proves Update's
// switch statement itself reaches each case, which none of those direct
// calls do.
func TestUpdateRoutesEveryAsyncPortMsgThroughTheSwitch(t *testing.T) {
	for _, tc := range []struct {
		name string
		msg  any
	}{
		{"noticeMsg", noticeMsg{event: uievent.Event{Kind: uievent.KindNotice, Body: uievent.NoticeBody{Text: "x"}}}},
		{"workflowStatusMsg", workflowStatusMsg{event: uievent.Event{Kind: uievent.KindWorkflowStatus, Body: uievent.WorkflowStatusBody{}}}},
		{"sessionMountedMsg", sessionMountedMsg{sessionID: "no-such-session"}},
		{"subagentTaskCancelResultMsg", subagentTaskCancelResultMsg{name: "t", ok: false, err: errors.New("boom")}},
		{"threadToolCallCancelResultMsg", threadToolCallCancelResultMsg{label: "l", ok: false, err: errors.New("boom")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestScreen(t)
			next, _ := s.Update(tc.msg)
			if _, ok := next.(Screen); !ok {
				t.Fatalf("Update(%T) returned %T, want Screen", tc.msg, next)
			}
		})
	}
}

// TestUpdateRoutesCompactionEventMsgThroughTheSwitch covers update's own
// compactionEventMsg case (conversation.go:474-475) - handleCompactionMessage
// already has direct tests, but never through Update's switch.
func TestUpdateRoutesCompactionEventMsgThroughTheSwitch(t *testing.T) {
	s := newTestScreen(t)
	s.compaction = compactionTestHandle{events: make(chan ports.CompactionEvent)}
	s.compactionSessionID = s.convID()

	next, _ := s.Update(compactionEventMsg{event: ports.CompactionEvent{SessionID: s.convID(), Done: true}})
	if _, ok := next.(Screen); !ok {
		t.Fatalf("Update(compactionEventMsg) returned %T, want Screen", next)
	}
}

// TestUpdateHandlesCompactionKeyBeforeFallingThroughToHandleKey covers
// update's own case tea.KeyPressMsg -> handleCompactionKey handled=true
// branch (conversation.go:439-440), distinct from handleCompactionKey's own
// direct tests which never go through Update's switch.
func TestUpdateHandlesCompactionKeyBeforeFallingThroughToHandleKey(t *testing.T) {
	s := newTestScreen(t)
	cancelled := false
	s.compaction = cancelTrackingHandle{events: make(chan ports.CompactionEvent), cancelled: &cancelled}

	next, _ := s.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	got, ok := next.(Screen)
	if !ok {
		t.Fatalf("Update(ctrl+c) returned %T, want Screen", next)
	}
	if !cancelled {
		t.Fatal("Update did not route ctrl+c through handleCompactionKey while a compaction was active")
	}
	_ = got
}

// TestHandleAppSettingsMsgDefaultsToNoOp covers handleAppSettingsMsg's
// fallthrough (conversation.go:274): a message type that is neither
// app.ScreenResumedMsg nor app.SettingsNoticeMsg must be a plain no-op, not a
// panic - the function's own switch has no default case, so an unmatched
// type falls all the way to the final `return s, nil`.
func TestHandleAppSettingsMsgDefaultsToNoOp(t *testing.T) {
	s := newTestScreen(t)
	next, cmd := s.handleAppSettingsMsg(struct{}{})
	if _, ok := next.(Screen); !ok {
		t.Fatalf("handleAppSettingsMsg(unmatched) returned %T, want Screen", next)
	}
	if cmd != nil {
		t.Fatalf("handleAppSettingsMsg(unmatched) cmd = %v, want nil", cmd)
	}
}
