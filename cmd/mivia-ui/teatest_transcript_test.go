// Transcript-mode integration tests: transcript mode (rule 6.2) and the
// rule 6.3 scrollback handover, driven through a real tea.Program. The
// handover test proves the ordering guarantee by reading the raw output
// byte stream: the alternate-screen exit must reach the terminal BEFORE
// the transcript dump does, or the dump lands on the alternate screen
// and is silently destroyed.
package main

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	teatest "github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/ui/screen/conversation"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/replay"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// transcriptFixture is a one-turn conversation whose tool output is long
// enough to render COLLAPSED in the cockpit: 20 body lines against a
// collapse threshold of 12. In the PAGER it is expanded, so the body
// marker appears both in pager frames and in the dump - the ordering
// proof below tells the two apart by the inline window they fall in.
func transcriptFixture() []uievent.Event {
	return []uievent.Event{
		{Kind: uievent.KindTurnStart, Body: uievent.TurnStartBody{Input: "run the upgrade check please"}},
		{Kind: uievent.KindToolOutput, Body: uievent.ToolOutputBody{
			ToolCallID: "c1",
			Chunk:      strings.Repeat("dumpmarker unique line alpha-omega\n", 20),
		}},
		{Kind: uievent.KindTextEnd, Body: uievent.TextEndBody{Text: "done: the upgrade check completed with no findings"}},
		{Kind: uievent.KindTurnEnd, Body: uievent.TurnEndBody{Reason: "completed"}},
	}
}

func newTranscriptRoot(t *testing.T) app.Model {
	t.Helper()
	themes, err := theme.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	th, err := resolveTheme(themes, config{themeName: "mivia-dark", themeExplicit: true})
	if err != nil {
		t.Fatal(err)
	}
	conv := replay.New(transcriptFixture(), 0)
	screen := conversation.New(th, theme.TierASCII, themes, conv, replay.NewApprover(), 80, nil)
	return app.New(screen, th, theme.TierASCII, themes)
}

// shadowStream drains a reader into an ordered buffer, so a test can
// wait on content without losing bytes it has already seen. teatest's
// own WaitFor consumes the reader it polls; every wait here reads the
// shadow copy instead, and the ordering assertions see the whole stream.
type shadowStream struct {
	src io.Reader
	buf bytes.Buffer
}

func (s *shadowStream) drain() {
	var tmp [512]byte
	for {
		n, err := s.src.Read(tmp[:])
		s.buf.Write(tmp[:n])
		if err != nil || n == 0 {
			return
		}
	}
}

// TestTranscriptModeSearchAndScrollbackHandover is the end-to-end proof
// for the blocking regression: ctrl+o opens the pager, `/` finds text
// the alternate screen had hidden, and `[` writes the conversation into
// native scrollback in the right order.
func TestTranscriptModeSearchAndScrollbackHandover(t *testing.T) {
	tm := teatest.NewTestModel(t, newTranscriptRoot(t), teatest.WithInitialTermSize(80, 24))
	shadow := &shadowStream{src: tm.Output()}
	waitShadow := func(cond func([]byte) bool) {
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

	tm.Type("hello")
	tm.Send(enterKey())
	waitShadow(func(b []byte) bool { return bytes.Contains(b, []byte("no findings")) })

	// Transcript mode: ctrl+o. The pager's hint line is on the frame.
	tm.Send(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	waitShadow(func(b []byte) bool { return bytes.Contains(b, []byte("/:search")) })

	// Search: `/` then a query that matches the user prompt; the match
	// count must appear. Enter closes the bar afterwards, or the next
	// `[` would type into the query instead of triggering the handover.
	tm.Send(tea.KeyPressMsg{Code: '/'})
	tm.Type("upgrade")
	waitShadow(func(b []byte) bool { return bytes.Contains(b, []byte("1 of")) })
	tm.Send(enterKey())

	// The handover. The 21st occurrence of the tool-body marker can
	// only come from the dump: the pager expanded the block into 20
	// body rows on screen, and the dump re-writes all 20 again.
	tm.Send(tea.KeyPressMsg{Code: '['})
	waitShadow(func(b []byte) bool { return bytes.Count(b, []byte("dumpmarker")) >= 21 })

	// A key returns to the pager, then quit the modal.
	tm.Send(tea.KeyPressMsg{Code: 'x'})
	quit(tm)
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))
	shadow.drain()

	out := shadow.buf.Bytes()
	// The tool body text also appears in pager frames drawn on the
	// alternate screen, so plain first-index ordering proves nothing.
	// The proof is the INLINE WINDOW: the bytes between leaving the
	// alternate screen (ESC[?1049l) and re-entering it (ESC[?1049h).
	// Only the handover hint and the dump are written there - the pager
	// itself never draws inline. If the dump were written while the
	// alternate screen was still held, its bytes could only appear
	// outside that window, and the window would hold no body text.
	altExit := []byte("\x1b[?1049l")
	altEnter := []byte("\x1b[?1049h")
	dump := []byte("dumpmarker unique line alpha-omega")

	exitAt := bytes.Index(out, altExit)
	if exitAt < 0 {
		t.Fatalf("no alternate-screen exit in the output stream; the handover never released the terminal:\n%s", out)
	}
	windowEnd := bytes.Index(out[exitAt:], altEnter)
	if windowEnd < 0 {
		windowEnd = len(out) - exitAt
	}
	window := out[exitAt : exitAt+windowEnd]
	if dumpAt := bytes.Index(window, dump); dumpAt < 0 {
		t.Fatalf("the dump never reached the released terminal: no tool-body bytes inside the inline window after the alt-screen exit:\n%s", out)
	}
	if hintAt := bytes.Index(window, []byte("transcript written to the terminal scrollback")); hintAt < 0 {
		t.Error("the handover hint line never rendered in the inline window")
	}
}

func streamingFixture() []uievent.Event {
	return []uievent.Event{
		{Kind: uievent.KindTurnStart, Body: uievent.TurnStartBody{Input: "start live streaming turn"}},
		{Kind: uievent.KindNotice, Body: uievent.NoticeBody{Text: "initial-live-marker-1"}},
		{Kind: uievent.KindNotice, Body: uievent.NoticeBody{Text: "delayed-live-marker-2"}},
		{Kind: uievent.KindTextEnd, Body: uievent.TextEndBody{Text: "final-live-marker-3 completed successfully"}},
		{Kind: uievent.KindTurnEnd, Body: uievent.TurnEndBody{Reason: "completed"}},
	}
}

func newStreamingTranscriptRoot(t *testing.T, pace time.Duration) app.Model {
	t.Helper()
	themes, err := theme.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	th, err := resolveTheme(themes, config{themeName: "mivia-dark", themeExplicit: true})
	if err != nil {
		t.Fatal(err)
	}
	conv := replay.New(streamingFixture(), pace)
	screen := conversation.New(th, theme.TierASCII, themes, conv, replay.NewApprover(), 80, nil)
	return app.New(screen, th, theme.TierASCII, themes)
}

// TestTranscriptModeLiveStreaming proves that blocks arriving while the pager
// is open render in the pager view without reopening the pager.
func TestTranscriptModeLiveStreaming(t *testing.T) {
	tm := teatest.NewTestModel(t, newStreamingTranscriptRoot(t, 80*time.Millisecond), teatest.WithInitialTermSize(80, 24))
	shadow := &shadowStream{src: tm.Output()}
	waitShadow := func(cond func([]byte) bool) {
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

	tm.Type("start")
	tm.Send(enterKey())
	waitShadow(func(b []byte) bool { return bytes.Contains(b, []byte("initial-live-marker-1")) })

	// Open the pager while streaming is still active.
	tm.Send(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	waitShadow(func(b []byte) bool { return bytes.Contains(b, []byte("/:search")) })

	// The delayed events must arrive and appear in the pager frame without reopening it.
	waitShadow(func(b []byte) bool {
		return bytes.Contains(b, []byte("delayed-live-marker-2")) && bytes.Contains(b, []byte("final-live-marker-3"))
	})

	// Quit the pager and app.
	quit(tm)
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))
	shadow.drain()

	out := shadow.buf.Bytes()
	if !bytes.Contains(out, []byte("delayed-live-marker-2")) {
		t.Error("stream output missing delayed-live-marker-2")
	}
	if !bytes.Contains(out, []byte("final-live-marker-3")) {
		t.Error("stream output missing final-live-marker-3")
	}
}
