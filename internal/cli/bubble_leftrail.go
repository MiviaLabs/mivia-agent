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

// LeftRail is a 1-cell left-edge indicator (not a box border).
// Header-only application: first non-blank line carries the glyph.
type LeftRail struct {
	// Width is cells reserved (0 = off, MVP uses 1).
	Width int
	// Glyph is painted on the first content line.
	Glyph string
	// Char is reserved for full-height continuation rails (polish); unused in MVP.
	Char string
	// Color is a 256-color index string when Plain is false.
	Color string
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

// railForBlock returns static left chrome for a chat block kind.
// toolFailed only affects tool rails. System → status lines keep Width=0
// (glyph already in text). History never animates.
func railForBlock(kind ChatBlockKind, toolFailed bool, opts railOpts) LeftRail {
	r := LeftRail{Width: 1, ASCII: opts.ASCII, Plain: !opts.Color}
	switch kind {
	case ChatBlockUser:
		r.Glyph = "›"
		if opts.ASCII {
			r.Glyph = ">"
		}
		r.Color = chromeUser
	case ChatBlockAssistant:
		r.Glyph = "│"
		if opts.ASCII {
			r.Glyph = "|"
		}
		r.Color = chromeAssistant
	case ChatBlockThinking:
		r.Glyph = "┊"
		if opts.ASCII {
			r.Glyph = ":"
		}
		r.Color = chromeThinking
	case ChatBlockTool:
		if toolFailed {
			r.Glyph = "✗"
			if opts.ASCII {
				r.Glyph = "!"
			}
			r.Color = chromeError
		} else {
			r.Glyph = "◆"
			if opts.ASCII {
				r.Glyph = "*"
			}
			r.Color = chromeTools
		}
	case ChatBlockSystem:
		// "→ …" work status and ⚙ lines already carry markers.
		r.Width = 0
	case ChatBlockDivider:
		// Error dividers get a marker; plain done/cancel stay quiet.
		r.Width = 0
	default:
		r.Width = 0
	}
	return r
}

// railForDividerText enables error chrome when divider text looks like an error.
func railForDividerText(text string, opts railOpts) LeftRail {
	low := strings.ToLower(strings.TrimSpace(text))
	if strings.HasPrefix(low, "error") || strings.Contains(low, "error:") {
		r := LeftRail{Width: 1, Color: chromeError, ASCII: opts.ASCII, Plain: !opts.Color}
		r.Glyph = "!"
		return r
	}
	return LeftRail{Width: 0}
}

// paintRailCell returns the display cell (optionally colored).
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
	return lipgloss.NewStyle().Foreground(lipgloss.Color(rail.Color)).Render(g)
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

// applyLeftRailHeader paints the rail on the first non-blank content line.
// Blank vertical-pad lines are left unchanged. Continuation lines keep leading
// spaces (header-only MVP — no full-height rainbow stripe).
//
// For lines that already start with a single space (typical "  icon …"), the
// first space is replaced by the rail glyph so width stays stable.
// Otherwise the glyph is prepended with a trailing space (may grow width by 1–2).
func applyLeftRailHeader(lines []string, rail LeftRail) []string {
	if rail.Width == 0 || len(lines) == 0 {
		return lines
	}
	cell := paintRailCell(rail)
	out := append([]string(nil), lines...)
	for i, line := range out {
		if strings.TrimSpace(stripANSI(line)) == "" {
			continue
		}
		out[i] = injectRailOnLine(line, cell)
		break
	}
	return out
}

// injectRailOnLine places the rail in the first display column without growing
// width when the line has a 2-space (or 1-space) plain prefix. Styled lines that
// strip to a leading pad are rebuilt as cell + remainder (ANSI on the prefix
// pad is lost; content styles after the pad are not recoverable cheaply).
func injectRailOnLine(line, cell string) string {
	// Fast path: literal leading spaces (formatToolLine, unstyled pads).
	if strings.HasPrefix(line, "  ") {
		return cell + " " + line[2:]
	}
	if strings.HasPrefix(line, " ") {
		return cell + line[1:]
	}
	// Styled line (e.g. tuiThinkingStyle.Render("  ▸ …")): strip to measure pad.
	plain := stripANSI(line)
	if strings.HasPrefix(plain, "  ") {
		return cell + " " + plain[2:]
	}
	if strings.HasPrefix(plain, " ") {
		return cell + plain[1:]
	}
	// No pad to consume: prepend glyph only (1 cell). Prefer rare over +2 blowout.
	return cell + line
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
	return LeftRail{Width: 1, Glyph: "›", Color: chromeUser}
}

func RailAssistant() LeftRail {
	return LeftRail{Width: 1, Glyph: "│", Color: chromeAssistant}
}

func RailThinking() LeftRail {
	return LeftRail{Width: 1, Glyph: "┊", Color: chromeThinking}
}

func RailTools() LeftRail {
	return LeftRail{Width: 1, Glyph: "◆", Color: chromeTools}
}

func RailError() LeftRail {
	return LeftRail{Width: 1, Glyph: "!", Color: chromeError}
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
