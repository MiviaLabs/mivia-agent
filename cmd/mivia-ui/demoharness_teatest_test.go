// Full-frame integration tests for the demo harness driver
// (internal/uikit/demoharness), the demo binary's new default. These
// exercise the whole slice the demo-harness task requires: a multi-turn
// scripted scenario end to end, an approval round trip in both
// directions, cancel mid-stream keeping partial text, and slash
// commands that actually act (/theme, /model, /clear, /context).
//
// Every test uses shadowStream (defined in teatest_transcript_test.go):
// teatest.WaitFor consumes the reader it polls, so a second WaitFor
// call only ever sees bytes written after the first one returned. An
// assertion that needs to see content from an EARLIER point in the
// session - "the first streamed chunk is still on screen", "the model
// picker's title is gone now" - would silently pass or fail for the
// wrong reason against the raw reader. The shadow copy keeps every byte
// so these assertions see the whole session.
package main

import (
	"bytes"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	teatest "github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/ui/screen/conversation"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/demoharness"
)

// newDemoHarnessRoot builds the same root shape run's runCockpit
// constructs, against a fresh demoharness.Harness for scenario, so
// these tests exercise the production wiring (SetCommandRunner
// included), not a parallel test-only assembly.
func newDemoHarnessRoot(t *testing.T, scenario string, pace time.Duration) app.Model {
	t.Helper()
	themes, err := theme.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	th, err := resolveTheme(themes, config{themeName: "mivia-dark", themeExplicit: true})
	if err != nil {
		t.Fatal(err)
	}
	harness, err := demoharness.New(scenario, pace)
	if err != nil {
		t.Fatal(err)
	}
	screen := conversation.New(th, theme.TierASCII, themes, harness, harness, 80, nil)
	screen.SetCommands(mockCommands())
	screen.SetCommandRunner(harness)
	return app.New(screen, th, theme.TierASCII, themes)
}

// newDemoShadow wires a TestModel plus its shadowStream, so a caller
// never has to remember to drain before asserting.
func newDemoShadow(t *testing.T, scenario string, pace time.Duration) (*teatest.TestModel, *shadowStream, func(cond func([]byte) bool)) {
	t.Helper()
	tm := teatest.NewTestModel(t, newDemoHarnessRoot(t, scenario, pace), teatest.WithInitialTermSize(80, 24))
	shadow := &shadowStream{src: tm.Output()}
	wait := func(cond func([]byte) bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			shadow.drain()
			if cond(shadow.buf.Bytes()) {
				return
			}
		}
		t.Fatalf("condition not met in the shadow stream; captured so far:\n%s", shadow.buf.String())
	}
	return tm, shadow, wait
}

func contains(needle string) func([]byte) bool {
	return func(b []byte) bool { return bytes.Contains(b, []byte(needle)) }
}

// typeAndRun types a slash-command line and presses Enter twice.
//
// The composer's completion menu opens on any leading "/" and is still
// open once the typed text is an exact match for one of its own
// candidates (every command name below is). Rule 5.6 makes the first
// Enter accept the highlighted completion, not submit - "accepts and
// also submits" is the documented defect the rule exists to prevent -
// so a genuine submit needs a second Enter. tm.Type("/bogus") never
// matches a candidate, so this helper is only used for known commands.
func typeAndRun(tm *teatest.TestModel, line string) {
	tm.Type(line)
	tm.Send(enterKey())
	tm.Send(enterKey())
}

// TestDemoHarnessMultiTurnScenario proves the full-tour scenario
// answers DIFFERENTLY per turn, end to end through a real tea.Program:
// the first Send gets the small-talk reply, the second gets the
// tool-call reply.
func TestDemoHarnessMultiTurnScenario(t *testing.T) {
	tm, _, wait := newDemoShadow(t, "full-tour", 0)

	tm.Type("hi")
	tm.Send(enterKey())
	wait(contains("How can I help today?"))

	tm.Type("list files")
	tm.Send(enterKey())
	wait(contains("six packages"))

	quit(tm)
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))
}

// TestDemoHarnessApprovalApproved drives the approval scenario's
// tool.pending prompt to "once" (o) and proves the turn continues with
// the tool succeeding: the approved-path reply lands in the transcript.
func TestDemoHarnessApprovalApproved(t *testing.T) {
	tm, _, wait := newDemoShadow(t, "approval", 0)

	tm.Type("delete it")
	tm.Send(enterKey())
	wait(contains("approve run_command"))

	tm.Send(tea.KeyPressMsg{Code: 'o'})
	wait(contains("Removed the stale cache directory"))

	quit(tm)
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))
}

// TestDemoHarnessApprovalDenied drives the same prompt to "deny" (d)
// and proves the turn ends differently: no success text, a denial
// message instead.
func TestDemoHarnessApprovalDenied(t *testing.T) {
	tm, shadow, wait := newDemoShadow(t, "approval", 0)

	tm.Type("delete it")
	tm.Send(enterKey())
	wait(contains("approve run_command"))

	tm.Send(tea.KeyPressMsg{Code: 'd'})
	wait(contains("left the cache directory in place"))

	quit(tm)
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))
	shadow.drain()

	if bytes.Contains(shadow.buf.Bytes(), []byte("Removed the stale cache directory")) {
		t.Error("expected no success text after a denial")
	}
}

// TestDemoHarnessCancelMidStreamKeepsPartialText pins the task
// requirement end to end: esc cancels a streaming turn, the first
// streamed chunk stays on screen, and the reply's second chunk - which
// had not streamed yet - never arrives.
func TestDemoHarnessCancelMidStreamKeepsPartialText(t *testing.T) {
	pace := 3 * uikitconfig.TextDeltaFlushInterval
	tm, shadow, wait := newDemoShadow(t, "smalltalk", pace)

	tm.Type("hi")
	tm.Send(enterKey())
	// The first streamed chunk: "Good morning. " (a leading fragment,
	// not the full reply - the second chunk and the final text.end
	// carry the rest, and must not arrive after cancel).
	wait(contains("Good morning."))

	tm.Send(tea.KeyPressMsg{Code: tea.KeyEscape})
	wait(contains("cancelled"))

	quit(tm)
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))
	shadow.drain()

	out := shadow.buf.Bytes()
	if !bytes.Contains(out, []byte("Good morning.")) {
		t.Error("expected the first streamed chunk to stay on screen after cancel")
	}
	if bytes.Contains(out, []byte("How can I help today?")) {
		t.Error("expected the second chunk, which had not streamed yet, to never arrive after cancel")
	}
}

// TestDemoHarnessThemeCommandOpensPicker is one of the command-
// dispatch-seam proofs: /theme opens the existing theme picker modal
// instead of being sent as chat text.
func TestDemoHarnessThemeCommandOpensPicker(t *testing.T) {
	tm, shadow, wait := newDemoShadow(t, "smalltalk", 0)

	typeAndRun(tm, "/theme")
	wait(contains("select a theme"))

	quit(tm)
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))
	shadow.drain()

	if bytes.Contains(shadow.buf.Bytes(), []byte("How can I help today?")) {
		t.Error("expected /theme to never fall through to Send")
	}
}

// TestDemoHarnessModelCommandOpensPickerAndSelects is the second
// command-dispatch-seam proof: /model opens a picker over the fake
// model roster, and accepting the highlighted choice applies it and
// posts a confirmation notice - never sent as chat text.
func TestDemoHarnessModelCommandOpensPickerAndSelects(t *testing.T) {
	tm, shadow, wait := newDemoShadow(t, "smalltalk", 0)

	typeAndRun(tm, "/model")
	wait(contains("select a model"))
	tm.Send(enterKey()) // accept the first (highlighted) model
	wait(contains("model set to"))

	quit(tm)
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))
	shadow.drain()

	if bytes.Contains(shadow.buf.Bytes(), []byte("How can I help today?")) {
		t.Error("expected /model to never fall through to Send")
	}
}

// TestDemoHarnessContextCommandShowsNotice is the third command-
// dispatch-seam proof: /context renders the fake usage state as a
// transcript notice instead of being sent as chat text.
func TestDemoHarnessContextCommandShowsNotice(t *testing.T) {
	tm, shadow, wait := newDemoShadow(t, "smalltalk", 0)

	typeAndRun(tm, "/context")
	wait(contains("Context usage"))

	quit(tm)
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))
	shadow.drain()

	if bytes.Contains(shadow.buf.Bytes(), []byte("How can I help today?")) {
		t.Error("expected /context to never fall through to Send")
	}
}

// TestDemoHarnessClearCommandEmptiesTranscript is the fifth command
// proof, isolated in its own session: /clear must empty the transcript
// view. The whole session's byte HISTORY still legitimately contains
// the pre-clear reply (it really was rendered once), so the proof
// looks only at bytes written AFTER /clear was accepted: the cockpit
// repaints its fixed-height window on every frame (rule 2.8), so a
// cleared transcript stops re-emitting that text in every frame from
// here on.
func TestDemoHarnessClearCommandEmptiesTranscript(t *testing.T) {
	tm, shadow, wait := newDemoShadow(t, "smalltalk", 0)

	tm.Type("hi")
	tm.Send(enterKey())
	wait(contains("How can I help today?"))

	shadow.drain()
	markerLen := shadow.buf.Len()

	typeAndRun(tm, "/clear")
	// Force at least one more frame after the clear: a window resize
	// is a cheap, deterministic repaint trigger that does not depend on
	// timing.
	tm.Send(tea.WindowSizeMsg{Width: 80, Height: 24})
	wait(func(b []byte) bool { return len(b) > markerLen })

	quit(tm)
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))
	shadow.drain()

	after := shadow.buf.Bytes()[markerLen:]
	if bytes.Contains(after, []byte("How can I help today?")) {
		t.Error("expected /clear to stop the prior reply from appearing in any frame drawn after it")
	}
}

// TestDemoHarnessUnknownCommandNeverSendsAsChat pins the hard
// requirement: an unrecognised "/x" shows a visible error and never
// falls through to Send (which would start a turn and show the fake's
// reply text instead).
func TestDemoHarnessUnknownCommandNeverSendsAsChat(t *testing.T) {
	tm, shadow, wait := newDemoShadow(t, "smalltalk", 0)

	tm.Type("/bogus")
	tm.Send(enterKey())
	wait(contains("unknown command /bogus"))

	quit(tm)
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))
	shadow.drain()

	if bytes.Contains(shadow.buf.Bytes(), []byte("Good morning")) {
		t.Error("expected /bogus to never start a turn (no small-talk reply should appear)")
	}
}
