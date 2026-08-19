// Full-frame integration tests using teatest, driving the real
// tea.Program - the production terminal-input/output loop, not a direct
// Update() call.
//
// These originally surfaced a real gap: internal/ui/component/transcript
// rendered its entire history as one growing View() string, which does
// not compose with Bubble Tea's inline redraw (relative cursor movement
// plus erase) - content taller than the terminal got erased on every
// repaint. Fixed by having transcript emit each finalized block once via
// CommitMsg -> tea.Println (native terminal scrollback), so Model.View()
// only ever renders the live streaming tail. A normal 80x24 terminal
// below now genuinely proves the fix, not a workaround around it.
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
	th, err := resolveTheme(themes, config{themeName: "mivia-dark", themeExplicit: true})
	if err != nil {
		t.Fatal(err)
	}
	events, err := stream.DefaultFixture()
	if err != nil {
		t.Fatal(err)
	}
	conv := replay.New(events, 0) // no pace: the test doesn't want to wait out real replayPace
	screen := conversation.New(th, theme.TierASCII, themes, conv, replay.NewApprover(), 80, nil)
	return app.New(screen, th, theme.TierASCII, themes)
}

// TestInteractiveSendAndReceive drives the real app.Model through a real
// tea.Program (teatest), the first fully end-to-end test in this repo
// for the new UI: a real terminal-input/output loop, not a direct
// Update() call. It types a message, presses enter, and waits for the
// replayed reply to actually appear in the rendered output.
func TestInteractiveSendAndReceive(t *testing.T) {
	// A normal terminal size: the fixture's rendered history is ~35
	// lines, well past 24 rows, so this genuinely exercises the
	// CommitMsg/tea.Println scrollback fix (see the package doc comment)
	// rather than working around a short terminal.
	tm := teatest.NewTestModel(t, newDemoRoot(t), teatest.WithInitialTermSize(80, 24))

	tm.Type("hello")
	tm.Send(enterKey())

	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return bytes.Contains(out, []byte("bounded retry"))
	}, teatest.WithDuration(5*time.Second))

	tm.Send(quitKey())
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

	tm.Send(quitKey())
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))

	out, err := readAll(tm)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, []byte("bounded retry")) {
		t.Errorf("expected no reply for an empty send, got:\n%s", out)
	}
}
