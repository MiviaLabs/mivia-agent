package newtui

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// TestFullDiskNotifierBridgeNilGuard mirrors TestMouseNotifierBridgeNilGuard
// for wireFullDiskNotifier's own nil-store guard.
func TestFullDiskNotifierBridgeNilGuard(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("a nil store must skip the notifier bridge, got panic: %v", r)
		}
	}()
	wireFullDiskNotifier(nil, nil)
}

// captureNoticeModel records every SettingsNoticeMsg the program receives,
// so the test can prove wireFullDiskNotifier's wired closure actually
// reached the running program's update loop.
type captureNoticeModel struct {
	got chan app.SettingsNoticeMsg
}

func (m captureNoticeModel) Init() tea.Cmd { return nil }
func (m captureNoticeModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if sn, ok := msg.(app.SettingsNoticeMsg); ok {
		select {
		case m.got <- sn:
		default:
		}
	}
	return m, nil
}
func (m captureNoticeModel) View() tea.View { return tea.NewView("") }

// TestFullDiskNotifierBridgeSendsToProgram exercises the wired closure
// itself: a live full-disk grant through the store's own Settings facade
// must reach the running program's update loop as the never-silent
// disclosure. A registered no-op re-arm is what makes
// AgentSessionState.ApplyFullDisk report true, the condition
// applySetFullDiskAccess gates the notifier on.
func TestFullDiskNotifierBridgeSendsToProgram(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	state := &cliagents.AgentSessionState{}
	state.SetFullDiskReArm(func(bool) {})

	store := uiadapter.NewSettingsStore(nil, nil, state)
	model := captureNoticeModel{got: make(chan app.SettingsNoticeMsg, 4)}
	p := tea.NewProgram(model, tea.WithInput(strings.NewReader("")), tea.WithOutput(io.Discard))
	wireFullDiskNotifier(store, p)

	done := make(chan struct{})
	go func() {
		_, _ = p.Run()
		close(done)
	}()
	defer func() {
		p.Quit()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Error("program did not stop after Quit")
		}
	}()

	h, err := store.Settings().General.Apply(context.Background(), ports.ScopeUser, ports.SetFullDiskAccess{On: true})
	if err != nil {
		t.Fatal(err)
	}
	for range h.Events() {
	}

	select {
	case sn := <-model.got:
		if !strings.Contains(strings.ToLower(sn.Text), "full disk") {
			t.Fatalf("SettingsNoticeMsg.Text = %q, want the full-disk disclosure", sn.Text)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the SetFullDiskAccess edit never reached the program's update loop")
	}
}
