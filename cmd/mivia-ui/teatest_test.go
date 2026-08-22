// Full-frame integration tests using teatest, driving the real
// tea.Program - the production terminal-input/output loop, not a direct
// Update() call.
//
// The cockpit draws to the alternate screen, so the terminal has no
// scrollback of its own here. That makes these tests the only place that
// proves the transcript keeps what scrolls off: the assertions below
// check the tail is drawn AND that jumping to the top brings back
// content that left the screen long before.
package main

import (
	"bytes"
	"io"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	teatest "github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/ui/screen/conversation"
	"github.com/MiviaLabs/mivia-agent/internal/ui/stream"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/replay"
)

func enterKey() tea.KeyPressMsg { return tea.KeyPressMsg{Code: tea.KeyEnter} }
func quitKey() tea.KeyPressMsg  { return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl} }
func quit(tm *teatest.TestModel) {
	tm.Send(quitKey())
	tm.Send(quitKey())
}
func topKey() tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: tea.KeyHome, Mod: tea.ModCtrl}
}

func readAll(tm *teatest.TestModel) ([]byte, error) {
	return io.ReadAll(tm.Output())
}

// newDemoRoot builds the exact same root model run() constructs for the
// interactive path, so this test exercises production wiring, not a
// parallel test-only assembly.
func newDemoRoot(t *testing.T) app.Model {
	t.Helper()
	themes, err := theme.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	th, err := resolveTheme(themes, cfg{themeName: "mivia-dark", themeExplicit: true})
	if err != nil {
		t.Fatal(err)
	}
	events, err := stream.DefaultFixture()
	if err != nil {
		t.Fatal(err)
	}
	conv := replay.New(events, 0)
	screen := conversation.New(th, theme.TierASCII, themes, conv, replay.NewApprover(), 80, nil)
	return app.New(screen, th, theme.TierASCII, themes)
}

func TestInteractiveSendAndReceive(t *testing.T) {
	tm := teatest.NewTestModel(t, newDemoRoot(t), teatest.WithInitialTermSize(80, 24))

	tm.Type("hello")
	tm.Send(enterKey())

	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return bytes.Contains(out, []byte("1284 in"))
	}, teatest.WithDuration(5*time.Second))

	tm.Send(topKey())
	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return bytes.Contains(out, []byte("bounded retry"))
	}, teatest.WithDuration(5*time.Second))

	quit(tm)
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))
}

// TestInteractiveEmptyEnterIsNoOp proves pressing enter with nothing
// typed does not start a turn - the transcript stays empty, only the
// composer's own frame renders.
func TestInteractiveEmptyEnterIsNoOp(t *testing.T) {
	tm := teatest.NewTestModel(t, newDemoRoot(t), teatest.WithInitialTermSize(80, 24))

	tm.Send(enterKey())
	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return bytes.Contains(out, []byte(">"))
	}, teatest.WithDuration(2*time.Second))

	quit(tm)
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))

	out, err := readAll(tm)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, []byte("bounded retry")) {
		t.Errorf("expected no reply for an empty send, got:\n%s", out)
	}
}
