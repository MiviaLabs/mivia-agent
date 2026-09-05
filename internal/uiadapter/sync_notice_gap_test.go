package uiadapter

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

func TestPushNoticeEventNoopsWithoutAChannel(t *testing.T) {
	var nilPool *SessionPool
	nilPool.pushNoticeEvent(uievent.Event{}) // must not panic

	p := &SessionPool{}
	p.pushNoticeEvent(uievent.Event{}) // notices is nil, must not panic
}

func TestPushWorkflowStatusNoopsWithoutAChannel(t *testing.T) {
	var nilPool *SessionPool
	nilPool.pushWorkflowStatus(uievent.Event{}) // must not panic

	p := &SessionPool{}
	p.pushWorkflowStatus(uievent.Event{}) // workflowStatusCh is nil, must not panic
}
