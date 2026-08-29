package newtui

import (
	"context"
	"io"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/config"
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

// TestMouseNotifierBridgeSendsToProgram exercises the wired closure
// itself: a real SetMouse edit through the store's own Settings facade
// must reach p.Send. The program uses the process's real stdin/stdout
// (not a TTY in a test run), which fails Run() fast rather than
// blocking - the same behavior TestRunTUICallsBuildApp relies on.
func TestMouseNotifierBridgeSendsToProgram(t *testing.T) {
	store := uiadapter.NewSettingsStore(nil, nil, nil)
	p := tea.NewProgram(blankModel{})
	wireMouseNotifier(store, p)

	done := make(chan struct{})
	go func() {
		_, _ = p.Run()
		close(done)
	}()
	<-done

	h, err := store.Settings().General.Apply(context.Background(), ports.ScopeUser, ports.SetMouse{On: false})
	if err != nil {
		t.Fatal(err)
	}
	for range h.Events() {
	}
}
