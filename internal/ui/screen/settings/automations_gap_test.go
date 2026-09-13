package settings

import (
	"errors"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

type dummyRunHandle struct {
	ch chan ports.Run
}

func (d dummyRunHandle) Events() <-chan ports.Run {
	return d.ch
}

func (d dummyRunHandle) Cancel() {}

func TestAutomationsWatchEndedMsg(t *testing.T) {
	ch := make(chan ports.Run)
	close(ch)
	handle := dummyRunHandle{ch: ch}
	cmd := watchNext(handle)
	msg := cmd()
	if ended, ok := msg.(automationsWatchEndedMsg); !ok || ended.handle != handle {
		t.Fatalf("expected automationsWatchEndedMsg, got %#v", msg)
	}
}

func TestAutomationsCancelRunError(t *testing.T) {
	h := newMockSettings()
	h.automationsApplyErr = errors.New("apply boom")
	sec := newTestAutomationsSection(t, h.SettingsAdapters().Automations)
	sec.liveRun = &ports.Run{
		ID:    "run-1",
		State: ports.RunRunning,
	}
	next, cmd := sec.cancelRun()
	if cmd != nil {
		t.Fatalf("expected nil cmd on error")
	}
	autoSec, ok := next.(*automationsSection)
	if !ok {
		t.Fatalf("expected *automationsSection, got %T", next)
	}
	if autoSec.notice != "apply boom" {
		t.Fatalf("expected notice 'apply boom', got %q", autoSec.notice)
	}
}
