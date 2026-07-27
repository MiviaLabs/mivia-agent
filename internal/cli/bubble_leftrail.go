package cli

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Chrome color tokens — single map from screen action → 256-color index.
// Reuse brandColor* so status bar and bubble chrome stay aligned.
const (
	chromeUser      = brandColorStream   // "12" blue — user
	chromeAssistant = brandColorCancel   // "8" dim — quiet speech
	chromeThinking  = brandColorMulti    // "13" magenta
	chromeTools     = brandColorTools    // "11" yellow
	chromeOK        = brandColorQueue    // "10" green
	chromeError     = brandColorError    // "9" red
	chromeAwait     = brandColorThinking // "14" cyan
)

// LeftRail is a left-edge indicator painted on every line of a block
// (full height). When the block is collapsed, render produces one line so
// the rail appears only on that single collapsed row.
type LeftRail struct {
	// Width is cells reserved (0 = off, 1 = single thick bar).
	Width int
	// Glyph is the full-height bar/mark painted on every line.
	Glyph string
	// Char is an optional alternate for continuation (defaults to Glyph).
	Char string
	// Color is a 256-color index string when Plain is false.
	Color string
	// Bold forces bold lipgloss weight (thicker perceived rail).
	Bold bool
	// ASCII forces ASCII glyph variants.
	ASCII bool
	// Plain disables color (NO_COLOR / dumb TERM).
	Plain bool
}

// railOpts controls environment-sensitive chrome.
type railOpts struct {
	ASCII bool
	Color bool
}

// chromeRenderOpts mirrors tool render env (NO_COLOR, TERM=dumb).
func chromeRenderOpts() railOpts {
	o := terminalToolRenderOptions()
	return railOpts{ASCII: o.ASCII, Color: o.Color}
}

// railForBlock returns static full-height left chrome for a chat block kind.
// toolFailed only affects tool rails. System → status lines keep Width=0
// (glyph already in text). History never animates.
//
// Glyphs are heavy bar forms so the rail reads bold at full height (not thin │).
func railForBlock(kind ChatBlockKind, toolFailed bool, opts railOpts) LeftRail {
	r := LeftRail{Width: 1, ASCII: opts.ASCII, Plain: !opts.Color, Bold: true}
	// Thick solid bar (Grok-like accent line). ASCII: # or |
	bar := "▌" // left half block — heavy solid column
	if opts.ASCII {
		bar = "#"
	}
	switch kind {
	case ChatBlockUser:
		r.Glyph = bar
		r.Color = chromeUser
	case ChatBlockAssistant:
		r.Glyph = bar
		r.Color = chromeAssistant
	case ChatBlockThinking:
		r.Glyph = bar
		r.Color = chromeThinking
	case ChatBlockTool:
		r.Glyph = bar
		if toolFailed {
			r.Color = chromeError
		} else {
			r.Color = chromeTools
		}
	case ChatBlockSystem:
		// "→ …" work status and ⚙ lines already carry markers.
		r.Width = 0
	case ChatBlockDivider:
		r.Width = 0
	default:
		r.Width = 0
	}
	r.Char = r.Glyph
	return r
}

// railForDividerText enables error chrome when divider text looks like an error.
func railForDividerText(text string, opts railOpts) LeftRail {
	low := strings.ToLower(strings.TrimSpace(text))
	if strings.HasPrefix(low, "error") || strings.Contains(low, "error:") {
		r := LeftRail{Width: 1, Color: chromeError, ASCII: opts.ASCII, Plain: !opts.Color, Bold: true}
		r.Glyph = "!"
		if opts.ASCII {
			r.Glyph = "!"
		}
		r.Char = r.Glyph
		return r
	}
	return LeftRail{Width: 0}
}

// paintRailCell returns the display cell (bold + colored when enabled).
func paintRailCell(rail LeftRail) string {
	g := rail.Glyph
	if g == "" {
		g = rail.Char
	}
	if g == "" {
		return " "
	}
	if rail.Plain || rail.Color == "" {
		return g
	}
	st := lipgloss.NewStyle().Foreground(lipgloss.Color(rail.Color))
	if rail.Bold {
		st = st.Bold(true)
	}
	return st.Render(g)
}

// leftPadWithRail builds the left pad string: [glyph][spaces…] using padLeft cells.
// When rail is off, returns plain spaces of length padLeft.
func leftPadWithRail(padLeft int, rail LeftRail) string {
	if padLeft < 0 {
		padLeft = 0
	}
	if rail.Width == 0 || padLeft == 0 {
		return strings.Repeat(" ", padLeft)
	}
	cell := paintRailCell(rail)
	rest := padLeft - 1
	if rest < 0 {
		rest = 0
	}
	return cell + strings.Repeat(" ", rest)
}

// applyLeftRail paints a full-height rail on every line of the block
// (including vertical padding lines so the accent is continuous).
// Collapsed blocks only have one line, so the rail appears once.
//
// Prefer width-neutral injection when lines have a 2-space prefix.
func applyLeftRail(lines []string, rail LeftRail) []string {
	if rail.Width == 0 || len(lines) == 0 {
		return lines
	}
	cell := paintRailCell(rail)
	out := make([]string, len(lines))
	for i, line := range lines {
		// Empty pad lines: still paint rail so the bar is full height.
		if line == "" || strings.TrimSpace(stripANSI(line)) == "" {
			// Preserve width-ish: rail + spaces if original had width.
			if plain := stripANSI(line); plain != "" {
				out[i] = injectRailOnLine(line, cell)
			} else {
				out[i] = cell
			}
			continue
		}
		out[i] = injectRailOnLine(line, cell)
	}
	return out
}

// applyLeftRailHeader is kept as an alias for callers/tests that used the old name.
// Behavior is full-height (same as applyLeftRail).
func applyLeftRailHeader(lines []string, rail LeftRail) []string {
	return applyLeftRail(lines, rail)
}

// injectRailOnLine places the rail in the first display column without growing
// width when the line has a leading space pad. Preserves ANSI (background fill
// on user pad rows, thinking styles) by rewriting the first display cell in
// place rather than stripping the whole line.
func injectRailOnLine(line, cell string) string {
	// Fast path: literal leading spaces (formatToolLine, unstyled pads).
	if strings.HasPrefix(line, "  ") {
		return cell + " " + line[2:]
	}
	if strings.HasPrefix(line, " ") {
		return cell + line[1:]
	}
	// ANSI-aware: keep SGR (e.g. bg fill) and replace first display space,
	// or prepend when there is no leading pad.
	return injectRailANSI(line, cell)
}

// injectRailANSI walks line, copies CSI sequences, and replaces the first
// display cell when it is a space (width-neutral). Otherwise prepends cell.
func injectRailANSI(line, cell string) string {
	var b strings.Builder
	b.Grow(len(line) + len(cell))
	i := 0
	for i < len(line) {
		if line[i] == '\033' {
			// Copy full CSI sequence ESC [ ... final-byte
			j := i + 1
			if j < len(line) && line[j] == '[' {
				j++
				for j < len(line) {
					c := line[j]
					j++
					if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
						break
					}
				}
			} else if j < len(line) {
				j++ // ESC + one char (rare)
			}
			b.WriteString(line[i:j])
			i = j
			continue
		}
		// First display byte.
		if line[i] == ' ' {
			b.WriteString(cell)
			b.WriteString(line[i+1:])
			return b.String()
		}
		// No leading pad: prepend (may grow width by 1).
		b.WriteString(cell)
		b.WriteString(line[i:])
		return b.String()
	}
	// Line was only CSI / empty — still paint rail for full-height continuity.
	if b.Len() == 0 {
		return cell
	}
	return cell + b.String()
}

// blockToolFailed heuristically detects failed tool blocks from text/name.
func blockToolFailed(b ChatBlock) bool {
	if b.Kind != ChatBlockTool {
		return false
	}
	low := strings.ToLower(b.Text + " " + b.Rendered)
	return strings.Contains(low, "error") ||
		strings.Contains(low, "exit=1") ||
		strings.Contains(low, "failed") ||
		strings.Contains(low, "exit=error")
}

// Presets for MessageBubble.Style.LeftRail (static copies; env applied at paint).
func RailUser() LeftRail {
	return LeftRail{Width: 1, Glyph: "▌", Char: "▌", Color: chromeUser, Bold: true}
}

func RailAssistant() LeftRail {
	return LeftRail{Width: 1, Glyph: "▌", Char: "▌", Color: chromeAssistant, Bold: true}
}

func RailThinking() LeftRail {
	return LeftRail{Width: 1, Glyph: "▌", Char: "▌", Color: chromeThinking, Bold: true}
}

func RailTools() LeftRail {
	return LeftRail{Width: 1, Glyph: "▌", Char: "▌", Color: chromeTools, Bold: true}
}

func RailError() LeftRail {
	return LeftRail{Width: 1, Glyph: "!", Char: "!", Color: chromeError, Bold: true}
}

// stripANSI removes ANSI escape sequences from a string.
func stripANSI(s string) string {
	var out strings.Builder
	skip := 0
	for _, r := range s {
		if r == '\033' {
			skip = 2
			continue
		}
		if skip > 0 {
			if skip == 2 && r == '[' {
				skip = 3
				continue
			}
			if skip >= 3 {
				if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
					skip = 0
				} else {
					skip++
				}
				continue
			}
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}
