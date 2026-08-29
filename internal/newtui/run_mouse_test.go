package newtui

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

func ptr(v bool) *bool { return &v }

func TestMouseEnabledPrecedence(t *testing.T) {
	cases := []struct {
		name string
		res  *config.Resolved
		env  []string
		want bool
	}{
		{"default on", nil, nil, true},
		{"toml off", &config.Resolved{TUI: config.TUIConfig{Mouse: ptr(false)}}, nil, false},
		{"toml on", &config.Resolved{TUI: config.TUIConfig{Mouse: ptr(true)}}, nil, true},
		{"env overrides toml on", &config.Resolved{TUI: config.TUIConfig{Mouse: ptr(true)}}, []string{"MIVIA_MOUSE=0"}, false},
		{"env overrides toml off", &config.Resolved{TUI: config.TUIConfig{Mouse: ptr(false)}}, []string{"MIVIA_MOUSE=1"}, true},
		{"env truthy spellings", nil, []string{"TERM=xterm", "MIVIA_MOUSE=yes"}, true},
		{"env falsy spelling", nil, []string{"MIVIA_MOUSE=off"}, false},
		{"unrelated env ignored", &config.Resolved{TUI: config.TUIConfig{Mouse: ptr(false)}}, []string{"MIVIA_OTHER=1"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mouseEnabled(tc.res, tc.env); got != tc.want {
				t.Fatalf("mouseEnabled = %v, want %v", got, tc.want)
			}
		})
	}
}

// blankModel is the smallest tea.Model that can back a tea.Program in
// a test, since wiring the notifier only needs a *tea.Program to send
// to, never a running one.
type blankModel struct{}

func (blankModel) Init() tea.Cmd                       { return nil }
func (blankModel) Update(tea.Msg) (tea.Model, tea.Cmd) { return blankModel{}, nil }
func (blankModel) View() tea.View                      { return tea.NewView("") }

// TestMouseNotifierBridgeNilGuard covers the launcher's nil-store guard:
// a buildApp that could not produce a store must not register a bridge
// (a flipped guard would call SetMouseNotifier on a nil *SettingsStore
// and panic).
func TestMouseNotifierBridgeNilGuard(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("a nil store must skip the notifier bridge, got panic: %v", r)
		}
	}()
	wireMouseNotifier(nil, nil)
}

// TestMouseNotifierBridgeWiresRealStore covers the non-nil arm: a real
// store must have its notifier registered without panicking.
func TestMouseNotifierBridgeWiresRealStore(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("wiring a real store must not panic, got: %v", r)
		}
	}()
	store := uiadapter.NewSettingsStore(nil, nil, nil)
	p := tea.NewProgram(blankModel{}, tea.WithInput(strings.NewReader("")), tea.WithOutput(io.Discard))
	wireMouseNotifier(store, p)
}

// captureModel records every MouseCaptureMsg the program receives, so the
// test can prove the wired closure's p.Send actually reached the running
// program's update loop.
type captureModel struct {
	got chan app.MouseCaptureMsg
}

func (m captureModel) Init() tea.Cmd { return nil }
func (m captureModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if mc, ok := msg.(app.MouseCaptureMsg); ok {
		select {
		case m.got <- mc:
		default:
		}
	}
	return m, nil
}
func (m captureModel) View() tea.View { return tea.NewView("") }

// TestMouseNotifierBridgeSendsToProgram exercises the wired closure
// itself: a real SetMouse edit through the store's own Settings facade
// must reach the running program's update loop. The program gets explicit
// non-TTY input/output and an explicit Quit - an earlier version ran on
// the process's real stdin and relied on Run() failing fast off-TTY,
// which holds on linux but not on windows, where the program ran headless
// forever and the suite died on the 10-minute test timeout.
func TestMouseNotifierBridgeSendsToProgram(t *testing.T) {
	store := uiadapter.NewSettingsStore(nil, nil, nil)
	model := captureModel{got: make(chan app.MouseCaptureMsg, 4)}
	p := tea.NewProgram(model, tea.WithInput(strings.NewReader("")), tea.WithOutput(io.Discard))
	wireMouseNotifier(store, p)

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

	h, err := store.Settings().General.Apply(context.Background(), ports.ScopeUser, ports.SetMouse{On: false})
	if err != nil {
		t.Fatal(err)
	}
	for range h.Events() {
	}

	select {
	case mc := <-model.got:
		if mc.On {
			t.Fatalf("MouseCaptureMsg.On = true, want false (the applied setting)")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the SetMouse edit never reached the program's update loop")
	}
}
