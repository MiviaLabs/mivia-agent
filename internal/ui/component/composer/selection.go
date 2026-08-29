package composer

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"

	sel "github.com/MiviaLabs/mivia-agent/internal/ui/select"
)

// Mouse text selection over the input body. bubbles' textarea has no
// mouse support at all, so the composer owns the region: the screen
// injects the absolute rect (body rows only - menu and border rows are
// not selectable), the composer keeps the anchor/focus pair in
// body-local cells, paints the reverse-video highlight on the padded
// body lines inside View, and derives SelectedText from its own value
// re-wrapped with textarea's exact word-wrap rule.
//
// The textarea is a black box behind a value-receiver Update, so the
// drag state cannot ride through m.input.Update; it lives on the
// composer Model beside it. A text change invalidates a live selection
// (the rows under the anchor moved); the armed anchor is remembered by
// value so SetSelection can refuse a stale update even when the router
// hands the model back to us as a copy.

// SelectionRect returns the body region's current absolute screen rect.
func (m Model) SelectionRect() sel.Rect { return m.selRect }

// SetSelectionRect records the body rect. The owning screen calls it
// wherever it lays out the composer.
func (m *Model) SetSelectionRect(r sel.Rect) { m.selRect = r }

// SetSelection records the live drag selection in body-local cells. A
// text change invalidates: Update's value-comparison arm drops any
// selection whose armed value no longer matches, so a stale anchor can
// never copy drifted text.
func (m *Model) SetSelection(s sel.Selection) {
	if !s.Active {
		m.selState = sel.Selection{}
		return
	}
	m.selState = s
	m.selValue = m.Value()
}

// Selection reports the current selection, including the armed anchor.
func (m Model) Selection() sel.Selection { return m.selState }

// ClearSelection drops any selection and its highlight.
func (m *Model) ClearSelection() { m.selState = sel.Selection{} }

// HasSelection reports whether a selection is active.
func (m Model) HasSelection() bool { return m.selState.Active }

// SelectedText returns the plain stream text between anchor and focus,
// computed from the input value itself (never from a rendered string),
// wrapped into display rows aligned column-for-column with what View
// draws. Empty when inactive or when the text changed mid-drag.
func (m Model) SelectedText() string {
	if !m.selState.Active || m.selValue != m.Value() {
		return ""
	}
	from, to := m.selState.Ordered()
	rows := m.selectionRows()
	for i := range rows {
		rows[i] = strings.TrimRight(rows[i], " ")
	}
	return sel.StreamSelect(rows, from, to)
}

// highlightBodyLines wraps the selected cells of the textarea's own
// rendered rows in reverse video, before padding/FillBG/border join in
// View. The body rows are exactly what selectionRows describes, so the
// same region-local cells apply to both.
func (m Model) highlightBodyLines(body string) string {
	from, to := m.selState.Ordered()
	lines := strings.Split(body, "\n")
	return strings.Join(sel.HighlightLines(lines, from, to), "\n")
}

// selectionRows renders the visible body as plain display rows: prompt
// columns first (so cell coordinates match the frame exactly), then
// each logical line word-wrapped at the inner width, scrolled like the
// textarea's viewport, then blank rows up to the fixed height. Mirrors
// textarea.view for the parts that matter here: this composer never
// shows line numbers, wraps at the textarea's width, and pads to the
// full height.
func (m Model) selectionRows() []string {
	inner := m.input.Width()
	if inner < 1 {
		inner = 1
	}
	h := m.input.Height()
	if h < 1 {
		h = 1
	}
	var out []string
	for _, logical := range strings.Split(m.Value(), "\n") {
		for wi, row := range wrapLikeTextarea(logical, inner) {
			out = append(out, promptCells(promptWidth, wi == 0)+strings.TrimRight(row, " "))
		}
	}
	if start := m.input.ScrollYOffset(); start > 0 {
		if start > len(out) {
			start = len(out)
		}
		out = out[start:]
	}
	for len(out) < h {
		out = append(out, promptCells(promptWidth, false))
	}
	if len(out) > h {
		out = out[:h]
	}
	return out
}

// invalidateSelection drops a live selection because the text under it
// changed. Called from Update on any keystroke that mutates the value.
func (m *Model) invalidateSelection() {
	m.selState = sel.Selection{}
	m.selValue = ""
}

// promptCells is the prompt area as plain cells: the "> " glyph on the
// first logical row, two spaces of continuation indent after - matching
// SetPromptFunc. Display width decides how many cells it occupies,
// which is what the selection coordinates count.
func promptCells(w int, first bool) string {
	prompt := "> "
	if !first {
		prompt = ""
	}
	pw := ansi.StringWidth(prompt)
	if pw >= w {
		return ansi.Truncate(prompt, w, "")
	}
	return prompt + strings.Repeat(" ", w-pw)
}

// wrapLikeTextarea reproduces bubbles textarea's internal wrap rule
// (word wrap at width, spaces attached to the preceding word, trailing
// space kept after a wrap). textarea.wrap is unexported; this port
// keeps selection rows aligned with the drawn rows.
func wrapLikeTextarea(s string, width int) []string {
	runes := []rune(s)
	rows := raggedWrap(runes, width)
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = string(r)
	}
	return out
}

// raggedWrap is textarea.wrap verbatim in spirit: the loop below is the
// ported algorithm, using uniseg widths and the same trailing-space
// convention (a soft-wrapped row carries the spaces that followed the
// word that broke it).
func raggedWrap(runes []rune, width int) [][]rune {
	if width <= 0 {
		return [][]rune{runes}
	}
	var (
		lines  = [][]rune{{}}
		word   = []rune{}
		row    int
		spaces int
	)
	runeWidth := func(r rune) int { return uniseg.StringWidth(string(r)) }
	for _, r := range runes {
		if unicode.IsSpace(r) {
			spaces++
		} else {
			word = append(word, r)
		}

		if spaces > 0 {
			if uniseg.StringWidth(string(lines[row]))+uniseg.StringWidth(string(word))+spaces > width {
				row++
				lines = append(lines, []rune{})
				lines[row] = append(lines[row], word...)
				lines[row] = append(lines[row], repeatSpaces(spaces)...)
				spaces = 0
				word = nil
			} else {
				lines[row] = append(lines[row], word...)
				lines[row] = append(lines[row], repeatSpaces(spaces)...)
				spaces = 0
				word = nil
			}
		} else if len(word) > 0 {
			lastCharLen := runeWidth(word[len(word)-1])
			if uniseg.StringWidth(string(word))+lastCharLen > width {
				if len(lines[row]) > 0 {
					row++
					lines = append(lines, []rune{})
				}
				lines[row] = append(lines[row], word...)
				word = nil
			}
		}
	}

	if uniseg.StringWidth(string(lines[row]))+uniseg.StringWidth(string(word))+spaces >= width {
		lines = append(lines, []rune{})
		lines[row+1] = append(lines[row+1], word...)
		spaces++
		lines[row+1] = append(lines[row+1], repeatSpaces(spaces)...)
	} else {
		lines[row] = append(lines[row], word...)
		spaces++
		lines[row] = append(lines[row], repeatSpaces(spaces)...)
	}
	return lines
}

func repeatSpaces(n int) []rune {
	if n <= 0 {
		return nil
	}
	return []rune(strings.Repeat(" ", n))
}
