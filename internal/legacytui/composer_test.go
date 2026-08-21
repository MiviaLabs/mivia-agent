package legacytui

import (
	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// ─── Unit: renderComposer chrome ───────────────────────────────────────

func TestRenderComposer_SquareCornersAndNoHeaderLabel(t *testing.T) {
	t.Parallel()
	out := renderComposer("hello", 40, "provider/model")
	plain := cli.StripANSI(out)
	if !strings.Contains(plain, "┌") || !strings.Contains(plain, "┐") {
		t.Fatalf("missing square top corners:\n%q", plain)
	}
	if !strings.Contains(plain, "└") || !strings.Contains(plain, "┘") {
		t.Fatalf("missing square bottom corners:\n%q", plain)
	}
	if strings.ContainsAny(plain, "╭╮╰╯") {
		t.Fatalf("rounded corners must not render:\n%q", plain)
	}
	if strings.Contains(plain, "you") {
		t.Fatalf("'you' label must not render:\n%q", plain)
	}
	if !strings.Contains(plain, "hello") {
		t.Fatalf("expected body content 'hello':\n%q", plain)
	}
	if strings.Contains(plain, "queued") {
		t.Fatalf("composer must not carry status text:\n%q", plain)
	}
}

func TestRenderComposer_BottomRightShowsModelLabel(t *testing.T) {
	t.Parallel()
	out := renderComposer("hello", 40, "deepseek/deepseek-v4")
	plain := cli.StripANSI(out)
	lines := strings.Split(strings.TrimRight(plain, "\n"), "\n")
	bot := lines[len(lines)-1]
	if !strings.HasPrefix(bot, "└") || !strings.HasSuffix(bot, "┘") {
		t.Fatalf("bottom line must be square-bordered: %q", bot)
	}
	if !strings.Contains(bot, "deepseek/deepseek-v4") {
		t.Fatalf("bottom line must show the model label: %q", bot)
	}
	// Label sits on the right half of the line, not the left.
	if idx := strings.Index(bot, "deepseek"); idx < len(bot)/2 {
		t.Fatalf("model label must be right-aligned (idx %d of %d): %q", idx, len(bot), bot)
	}
}

func TestRenderComposer_LabelDroppedWhenNarrow(t *testing.T) {
	t.Parallel()
	out := renderComposer("x", 20, "a-very-long-provider/very-long-model-name")
	plain := cli.StripANSI(out)
	lines := strings.Split(strings.TrimRight(plain, "\n"), "\n")
	bot := lines[len(lines)-1]
	if !strings.HasPrefix(bot, "└") || !strings.HasSuffix(bot, "┘") {
		t.Fatalf("narrow bottom line must still be square: %q", bot)
	}
	if strings.Contains(bot, "provider") {
		t.Fatalf("label must be dropped when too narrow: %q", bot)
	}
}

func TestRenderComposer_FixedColorNoPhaseOrFocusFlips(t *testing.T) {
	// Not parallel: withANSI256 mutates the global lipgloss color profile.
	withANSI256(t)
	// The composer takes no phase or focus input anymore, and the border
	// style is one fixed color: the raw output must contain exactly the user
	// blue (256-color index 12) and no dim (8) or other foreground codes.
	out := renderComposer("draft", 40, "m")
	if !strings.Contains(out, "\x1b[94m") {
		t.Fatalf("composer must render the fixed user blue (SGR 94):\n%q", out)
	}
	if strings.Contains(out, "\x1b[90m") {
		t.Fatalf("composer must not dim the border:\n%q", out)
	}
	if strings.Contains(out, "38;5;") {
		t.Fatalf("composer must not use phase colors (38;5;):\n%q", out)
	}
}

func TestRenderComposer_EveryLineSpansFullWidth(t *testing.T) {
	t.Parallel()
	for _, w := range []int{20, 30, 40, 80} {
		out := renderComposer("hello", w, "provider/model")
		plain := cli.StripANSI(out)
		lines := strings.Split(strings.TrimRight(plain, "\n"), "\n")
		for i, line := range lines {
			if got := lipgloss.Width(line); got != w {
				t.Fatalf("width=%d line %d rendered %d cells: %q", w, i, got, line)
			}
		}
	}
}

func TestRenderComposer_NarrowTerminal_Clamps(t *testing.T) {
	t.Parallel()
	out := renderComposer("hello world", 10, "m")
	plain := cli.StripANSI(out)
	if !strings.Contains(plain, "┌") {
		t.Fatalf("narrow terminal must still render top corner:\n%q", plain)
	}
}

// ─── Unit: composerTopBorder direct tests ───────────────────────────────

func TestComposerTopBorder_SquareNoLabel(t *testing.T) {
	t.Parallel()
	out := cli.StripANSI(composerTopBorder(40, tuiUserStyle))
	if !strings.HasPrefix(out, "┌") || !strings.HasSuffix(out, "┐") {
		t.Fatalf("top border must be square corners: %q", out)
	}
	if strings.Contains(out, "you") {
		t.Fatalf("top border must not carry a label: %q", out)
	}
	if got := lipgloss.Width(out); got != 40 {
		t.Fatalf("top border width = %d, want 40", got)
	}
}

// ─── Unit: composerBottomBorder direct tests ────────────────────────────

func TestComposerBottomBorder_SquareNoStatusText(t *testing.T) {
	t.Parallel()
	out := composerBottomBorder(40, tuiUserStyle, "provider/model")
	plain := cli.StripANSI(out)
	if !strings.HasPrefix(plain, "└") {
		t.Fatalf("expected bottom-left corner, got %q", plain)
	}
	if !strings.HasSuffix(plain, "┘") {
		t.Fatalf("expected bottom-right corner, got %q", plain)
	}
	if strings.Contains(plain, "queued") {
		t.Fatalf("bottom border must not carry status text:\n%q", plain)
	}
	if got := lipgloss.Width(plain); got != 40 {
		t.Fatalf("bottom border width = %d, want 40: %q", got, plain)
	}
}

func TestComposerBottomBorder_LabelRightAligned(t *testing.T) {
	t.Parallel()
	out := cli.StripANSI(composerBottomBorder(40, tuiUserStyle, "deepseek/v4"))
	idx := strings.Index(out, "deepseek")
	if idx < 20 {
		t.Fatalf("label must sit on the right half (idx %d): %q", idx, out)
	}
	if !strings.HasSuffix(out, "┘") {
		t.Fatalf("bottom-right corner missing: %q", out)
	}
}

func TestComposerBottomBorder_Narrow(t *testing.T) {
	t.Parallel()
	out := composerBottomBorder(5, tuiUserStyle, "model")
	plain := cli.StripANSI(out)
	if !strings.HasPrefix(plain, "└") {
		t.Fatalf("narrow border must still render, got %q", plain)
	}
	if got := lipgloss.Width(plain); got != 5 {
		t.Fatalf("narrow border width = %d, want 5: %q", got, plain)
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

// ─── Unit: composerModelLabel ───────────────────────────────────────────

func TestComposerModelLabel_ProviderQualified(t *testing.T) {
	t.Parallel()
	m := newReadyChatModel(40, 80)
	m.session = newTestSessionForModel("deepseek-v4")
	if got := m.composerModelLabel(); got != "deepseek-v4" {
		t.Fatalf("composerModelLabel without provider = %q, want model only", got)
	}
}

// ─── Integration: TUI model in waiting state ────────────────────────────

// TestTUIWaitingComposer_Visible verifies that waiting-state renders a
// complete, visible composer with the new square chrome and no "you" text.
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

	plain := cli.StripANSI(out)
	if strings.Contains(plain, "you") {
		t.Errorf("composer must not show 'you' text:\n%q", plain)
	}
	// Textarea content must be visible
	if !strings.Contains(plain, "test draft") {
		t.Errorf("waiting state missing textarea content 'test draft'")
	}
	// Border corners must be square
	if !strings.Contains(plain, "┌") || !strings.Contains(plain, "└") {
		t.Errorf("waiting state composer missing square corners:\n%q", plain)
	}
	if strings.ContainsAny(plain, "╭╮╰╯") {
		t.Errorf("waiting state composer must not use rounded corners:\n%q", plain)
	}
	// Output must not be all whitespace (invisible composer)
	if len(strings.TrimSpace(plain)) == 0 {
		t.Fatalf("waiting state View() is all whitespace - invisible composer!")
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
	plain := cli.StripANSI(out)

	if strings.Contains(plain, "queued") {
		t.Errorf("idle composer must not show 'queued':\n%q", plain)
	}
	if strings.Contains(plain, "you") {
		t.Errorf("idle composer must not show 'you':\n%q", plain)
	}
}
