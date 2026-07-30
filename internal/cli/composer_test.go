package cli

import (
	"strings"
	"testing"
	"time"
)

// ─── Unit: renderComposer state machine ──────────────────────────────────

func TestRenderComposer_IdleFocused_ShowsYouLabel(t *testing.T) {
	t.Parallel()
	out := renderComposer("hello", 40, false, 0, true, phaseIdle, "", false)
	plain := stripANSI(out)
	if !strings.Contains(plain, "╭─") {
		t.Fatalf("missing top-left corner in idle focused output:\n%q", plain)
	}
	if !strings.Contains(plain, " you ") {
		t.Fatalf("expected ' you ' label in idle focused, got:\n%q", plain)
	}
	if !strings.Contains(plain, "hello") {
		t.Fatalf("expected body content 'hello':\n%q", plain)
	}
	if !strings.Contains(plain, "╰") {
		t.Fatalf("missing bottom border:\n%q", plain)
	}
	if strings.Contains(plain, "queued") {
		t.Fatalf("idle focused must not show queue text:\n%q", plain)
	}
}

func TestRenderComposer_IdleUnfocused_ShowsYouLabel(t *testing.T) {
	t.Parallel()
	out := renderComposer("hello", 40, false, 0, false, phaseIdle, "", false)
	plain := stripANSI(out)
	if !strings.Contains(plain, " you ") {
		t.Fatalf("expected ' you ' label in idle unfocused:\n%q", plain)
	}
	if strings.Contains(plain, "you · queue") {
		t.Fatalf("idle must not show queue header:\n%q", plain)
	}
}

func TestRenderComposer_Waiting_ShowsQueueLabelAndQueuedFooter(t *testing.T) {
	t.Parallel()
	out := renderComposer("hello", 40, true, 2, false, phaseThinking, "", false)
	plain := stripANSI(out)
	if !strings.Contains(plain, "you · queue") {
		t.Fatalf("waiting header must show 'you · queue':\n%q", plain)
	}
	if !strings.Contains(plain, "queued") {
		t.Fatalf("waiting footer must show 'queued':\n%q", plain)
	}
	if !strings.Contains(plain, "hello") {
		t.Fatalf("body content 'hello' must be present:\n%q", plain)
	}
}

func TestRenderComposer_Waiting_ShowsStepDetail(t *testing.T) {
	t.Parallel()
	out := renderComposer("draft", 50, true, 1, false, phaseTools, "searching", false)
	plain := stripANSI(out)
	if !strings.Contains(plain, "searching") {
		t.Fatalf("waiting must show stepDetail 'searching':\n%q", plain)
	}
	if strings.Contains(plain, "queued") {
		t.Fatalf("stepDetail must override 'queued':\n%q", plain)
	}
}

func TestRenderComposer_Waiting_StalledWarning(t *testing.T) {
	t.Parallel()
	out := renderComposer("draft", 50, true, 1, false, phaseThinking, "", true)
	plain := stripANSI(out)
	if !strings.Contains(plain, "stalled") {
		t.Fatalf("stalled warning must appear in footer:\n%q", plain)
	}
}

func TestRenderComposer_NotWaiting_NoQueueFooter(t *testing.T) {
	t.Parallel()
	out := renderComposer("hello", 30, false, 0, false, phaseIdle, "", false)
	plain := stripANSI(out)
	if strings.Contains(plain, "queued") {
		t.Fatalf("non-waiting must not show queue footer:\n%q", plain)
	}
	if strings.Contains(plain, "you · queue") {
		t.Fatalf("non-waiting must not show queue header:\n%q", plain)
	}
}

func TestRenderComposer_NarrowTerminal_Clamps(t *testing.T) {
	t.Parallel()
	out := renderComposer("hello world", 10, false, 0, true, phaseIdle, "", false)
	plain := stripANSI(out)
	if !strings.Contains(plain, "╭─") {
		t.Fatalf("narrow terminal must still render top corner:\n%q", plain)
	}
	if !strings.Contains(plain, " you ") {
		t.Fatalf("narrow terminal must show label:\n%q", plain)
	}
}

// ─── Unit: composerBottomBorder direct tests ─────────────────────────────

func TestComposerBottomBorder_NotWaiting(t *testing.T) {
	t.Parallel()
	out := composerBottomBorder(40, false, tuiUserStyle, "", false)
	plain := stripANSI(out)
	if !strings.HasPrefix(plain, "╰") {
		t.Fatalf("expected bottom-left corner, got %q", plain)
	}
	if !strings.HasSuffix(plain, "╯") {
		t.Fatalf("expected bottom-right corner, got %q", plain)
	}
	if strings.Contains(plain, "queued") {
		t.Fatalf("non-waiting bottom must not have queued text:\n%q", plain)
	}
}

func TestComposerBottomBorder_Waiting_DefaultQueued(t *testing.T) {
	t.Parallel()
	out := composerBottomBorder(40, true, tuiWaitingStyle, "", false)
	plain := stripANSI(out)
	if !strings.Contains(plain, "queued") {
		t.Fatalf("waiting bottom must show 'queued':\n%q", plain)
	}
}

func TestComposerBottomBorder_Waiting_StepDetail(t *testing.T) {
	t.Parallel()
	out := composerBottomBorder(50, true, tuiWaitingStyle, "analyzing", false)
	plain := stripANSI(out)
	if !strings.Contains(plain, "analyzing") {
		t.Fatalf("stepDetail 'analyzing' must appear:\n%q", plain)
	}
	if strings.Contains(plain, "queued") {
		t.Fatalf("stepDetail must override 'queued':\n%q", plain)
	}
}

func TestComposerBottomBorder_Waiting_StalledWarning(t *testing.T) {
	t.Parallel()
	out := composerBottomBorder(50, true, tuiWaitingStyle, "", true)
	plain := stripANSI(out)
	if !strings.Contains(plain, "stalled") {
		t.Fatalf("stalled warning must appear:\n%q", plain)
	}
}

func TestComposerBottomBorder_Narrow(t *testing.T) {
	t.Parallel()
	out := composerBottomBorder(5, true, tuiWaitingStyle, "", false)
	plain := stripANSI(out)
	if !strings.HasPrefix(plain, "╰") {
		t.Fatalf("narrow border must still render, got %q", plain)
	}
}

// ─── Unit: composer helper sanity ────────────────────────────────────────

func TestComposerOuterWidth_Minimum(t *testing.T) {
	t.Parallel()
	if got := composerOuterWidth(5); got != 20 {
		t.Fatalf("composerOuterWidth(5) = %d, want 20", got)
	}
	if got := composerOuterWidth(20); got != 20 {
		t.Fatalf("composerOuterWidth(20) = %d, want 20", got)
	}
	if got := composerOuterWidth(80); got != 80 {
		t.Fatalf("composerOuterWidth(80) = %d, want 80", got)
	}
}

func TestComposerInnerWidth(t *testing.T) {
	t.Parallel()
	// inner = composerOuterWidth(width) - 4, minimum 8.
	// Outer clamps to 20, so minimum inner is 16 with current minimum.
	if got := composerInnerWidth(20); got != 16 {
		t.Fatalf("composerInnerWidth(20) = %d, want 16", got)
	}
	if got := composerInnerWidth(10); got != 16 {
		t.Fatalf("composerInnerWidth(10) outer clamps to 20 → inner=%d, want 16", got)
	}
	if got := composerInnerWidth(80); got != 76 {
		t.Fatalf("composerInnerWidth(80) = %d, want 76", got)
	}
}

func TestComposerMaxHeight(t *testing.T) {
	t.Parallel()
	// The composer grows with the draft but is capped at 5 lines; tiny
	// terminals floor at a single line.
	if got := composerMaxHeight(10); got != 1 {
		t.Fatalf("composerMaxHeight(10) = %d, want 1 (min)", got)
	}
	if got := composerMaxHeight(20); got != 3 {
		t.Fatalf("composerMaxHeight(20) = %d, want 3", got)
	}
	if got := composerMaxHeight(60); got != 5 {
		t.Fatalf("composerMaxHeight(60) = %d, want 5 (cap)", got)
	}
	if got := composerMaxHeight(30); got != 5 {
		t.Fatalf("composerMaxHeight(30) = %d, want 5 (30/6=5)", got)
	}
}

// ─── Integration: TUI model in waiting state ────────────────────────────

// TestTUIWaitingComposer_Visible verifies that waiting-state renders a
// complete, visible composer. Catches regressions where the border uses
// an invisible color (ANSI 8 "bright black").
func TestTUIWaitingComposer_Visible(t *testing.T) {
	m := newReadyChatModel(40, 80)
	m.waiting = true
	m.turnStart = time.Now()
	m.awaitingFirstActivity = true
	m.followOutput = true
	m.textarea.SetValue("test draft")

	out := m.View()
	if out == "" {
		t.Fatalf("View() returned empty string in waiting state")
	}

	plain := stripANSI(out)

	// Must have "you · queue" label in header
	if !strings.Contains(plain, "you · queue") {
		t.Errorf("waiting state missing 'you · queue' header label")
	}
	// Must have "queued" in bottom border
	if !strings.Contains(plain, "queued") {
		t.Errorf("waiting state missing 'queued' footer indicator")
	}
	// Textarea content must be visible
	if !strings.Contains(plain, "test draft") {
		t.Errorf("waiting state missing textarea content 'test draft'")
	}
	// Border corners must be present
	if !strings.Contains(plain, "╭") || !strings.Contains(plain, "╰") {
		t.Errorf("waiting state composer missing border corners:\n%q", plain)
	}
	// Output must not be all whitespace (invisible composer)
	if len(strings.TrimSpace(plain)) == 0 {
		t.Fatalf("waiting state View() is all whitespace — invisible composer!")
	}
}

// TestTUIWaitingComposer_IdleReturn verifies that after waiting ends,
// the composer returns to idle state (no queue text).
func TestTUIWaitingComposer_IdleReturn(t *testing.T) {
	m := newReadyChatModel(40, 80)
	m.waiting = false
	m.turnStart = time.Now()
	m.stalledWarning = false
	m.textarea.SetValue("draft")

	out := m.View()
	plain := stripANSI(out)

	if strings.Contains(plain, "you · queue") {
		t.Errorf("idle composer must not show 'you · queue':\n%q", plain)
	}
	if strings.Contains(plain, "queued") {
		t.Errorf("idle composer must not show 'queued':\n%q", plain)
	}
	// Must still show label "you"
	if !strings.Contains(plain, " you ") {
		t.Errorf("idle composer must show ' you ' label:\n%q", plain)
	}
}
