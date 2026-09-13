package transcript

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// This file holds the tool block's outcome glyph (C3) and windowed
// "card" body (C4): column 1 of a tool block's header shows the call's
// outcome instead of a collapse marker, and a collapsed tool body shows
// a window of lines plus a trailing hint row instead of hiding
// everything. Split out of block.go, which carries the generic Block
// value and its Height/Render contract that these two features plug
// into (see the isToolBlock branches there).

// isToolBlock reports whether column 1 shows the call's outcome (C3)
// rather than the generic collapse marker: a block tied to a tool
// call's pending/running/settled lifecycle, tracked by CallID. Every
// other collapsible kind - plan, hook, notice, error, reasoning - keeps
// the plain v/>/blank marker, because none of them carries this
// ok/failed/pending/running vocabulary.
func (b Block) isToolBlock() bool {
	switch b.Kind {
	case uievent.KindToolPending, uievent.KindToolStart, uievent.KindToolEnd:
		return true
	default:
		return false
	}
}

// spinnerGlyphs is the 4-frame ASCII spinner wireframes-panes.md section 3
// specifies verbatim: "-|/-". The Unicode and ASCII glyph sets in that
// table are identical, so there is no tier branch here.
var spinnerGlyphs = [4]string{"-", "|", "/", "-"}

// spinnerGlyph picks a frame from the shared statusline tick (SpinnerFrame),
// never advancing one of its own.
func spinnerGlyph(frame int) string {
	if frame < 0 {
		frame = 0
	}
	return spinnerGlyphs[frame%len(spinnerGlyphs)]
}

// outcomeGlyph is the column-1 glyph for a tool block: "+" ok, "x"
// failed, "?" pending, or the running spinner. It reports false for
// every other kind, so the caller falls back to the plain collapse
// marker.
func (b Block) outcomeGlyph() (string, bool) {
	switch b.Kind {
	case uievent.KindToolPending:
		return "?", true
	case uievent.KindToolStart:
		return spinnerGlyph(b.SpinnerFrame), true
	case uievent.KindToolEnd:
		if b.Header.Role == theme.RoleDanger {
			return "x", true
		}
		return "+", true
	default:
		return "", false
	}
}

// columnOneGlyph is what draws in the header's marker column: a tool
// block's outcome glyph, or the ordinary collapse marker for every other
// collapsible kind. Collapse state no longer lives in column 1 for a
// tool block (C3) - a windowed tool card states its own truncation via
// the body hint row instead (C4).
func (b Block) columnOneGlyph() string {
	if g, ok := b.outcomeGlyph(); ok {
		return g
	}
	return b.collapseMarker()
}

// displayState is the header's State column for RENDERING: identical to
// Header.State except a settled, successful tool call drops the word
// "ok" (C3) - the "+" outcome glyph in column 1 already says so, and
// spelling it twice earns the row nothing. Header.State itself is left
// untouched; other logic (settledWork, workRunSpec) keys off Role and
// Kind, never off this string.
func (b Block) displayState() string {
	if b.Kind == uievent.KindToolEnd && b.Header.Role != theme.RoleDanger {
		return ""
	}
	return b.Header.State
}

// detailSuffix is a live tool row's own reason it has nothing to show
// yet (C5): the detail column's RoleFGSubtle suffix, distinct from the
// State column's bare "pending"/"running" word (C3). "waiting to run"
// while the call sits admitted but not yet dispatched, "waiting for
// result" once dispatch started. It disappears the moment the call
// settles, because Header.State is then "ok" or "failed", neither of
// which this switch recognizes.
//
// "requesting approval" is never used here: a policy-auto-approved
// pending call never enters the approval queue (events.go), so that
// phrase would misdescribe it.
func (b Block) detailSuffix() string {
	if !b.isToolBlock() {
		return ""
	}
	switch b.Header.State {
	case "pending":
		return "waiting to run"
	case "running":
		return "waiting for result"
	default:
		return ""
	}
}

// cardLayout is a tool block's body as shown, after C4's window: the
// rows actually drawn, and how many more the window left out (0 when
// nothing is hidden - the body fit, or there is no body at all).
type cardLayout struct {
	body   []string
	hidden int
}

// rows is the terminal row count the card itself contributes: the blank
// separator, the shown body lines, and the hint row when something is
// hidden. Zero when there is no body - a pending or running call with
// nothing to show yet draws no card at all, only its header.
func (c cardLayout) rows() int {
	if len(c.body) == 0 {
		return 0
	}
	n := 1 + len(c.body) // blank separator + shown body lines
	if c.hidden > 0 {
		n++
	}
	return n
}

// hintOffset is the row index, counted from the BLOCK's own top (the
// header is row 0), that the hint row draws on. It reports false when
// there is no hint - Height and this must never draw a different answer
// for the same block, so both read the same cardLayout.
func (c cardLayout) hintOffset() (int, bool) {
	if c.hidden == 0 {
		return 0, false
	}
	return 2 + len(c.body), true // header + blank + shown body rows
}

// card computes b's cardLayout at width: the full body wrapped to rows,
// windowed to CollapseThresholdLines when the block is collapsed. An
// expanded card, or one whose body already fits the window, hides
// nothing.
func (b Block) card(width int) cardLayout {
	rows := b.bodyRows(width)
	if len(rows) == 0 {
		return cardLayout{}
	}
	if !b.Collapsed || len(rows) <= uikitconfig.CollapseThresholdLines {
		return cardLayout{body: rows}
	}
	shown := rows[:uikitconfig.CollapseThresholdLines]
	return cardLayout{body: shown, hidden: len(rows) - len(shown)}
}

// padToWidth pads line with trailing spaces to exactly w display
// columns, so render.FillBG - which "colours the cells it is given and
// adds none" - fills the card's whole content width rather than a strip
// the length of the line's own text. w <= 0 returns line unchanged.
//
// It truncates, ANSI-aware, when line is already OVER w: block.go's
// Render calls this with a leading margin space prepended to line, and
// a line already at bodyRows' own ceiling - every diff line
// render.FormatDiffLines produces is pre-padded to exactly that ceiling
// - lands one column over once the margin is added. What gets cut is
// always that line's own trailing padding (bodyRows never hands this
// function more than one column of overhang), never real content.
func padToWidth(line string, w int) string {
	if w <= 0 {
		return line
	}
	if width := ansi.StringWidth(line); width < w {
		return line + strings.Repeat(" ", w-width)
	} else if width > w {
		return ansi.Truncate(line, w, "")
	}
	return line
}

// cardHint is the trailing row a windowed card draws below its body: the
// count of lines the window left out, plus the expand hint when the
// block holds focus (C4). Unfocused, the count alone still tells the
// reader what expanding would cost.
func cardHint(hidden int, focused bool) string {
	hint := fmt.Sprintf("… %d more lines", hidden)
	if focused {
		hint += "  space to expand"
	}
	return hint
}
