package clichat

import (
	"os"
	"strings"

	"charm.land/lipgloss/v2"
)

// Chrome color tokens - semantic status only (not tool names).
// Tools/steps default to neutral gray; yellow is for status-bar tools phase
// only. Values mirror internal/legacytui/brand.go's brandColor* constants
// (relocated here: needed unqualified by this package's own rail resolver
// and by internal/legacytui's rendering, which imports this package).
const (
	// ChromeNeutral is the default structural rail color.
	ChromeNeutral   = "8"   // dim gray - structure default
	chromeUser      = "33"  // vivid blue - user label (not rail)
	chromeAssistant = "8"   // quiet speech
	chromeThinking  = "170" // vivid magenta - rare multi
	chromeTools     = "178" // vivid gold - brand bar phase only
	chromeOK        = "40"  // vivid green - rare; not default tool OK
	// ChromeError is the strict-failure rail color.
	ChromeError = "160" // vivid red - strict failures only
)

// LeftRail is a left-edge indicator. Prefer header-only thin gray.
// Color encodes lifecycle, never tool name (read_file vs run_command).
type LeftRail struct {
	Width   int
	Glyph   string
	Char    string
	Color   string
	Bold    bool
	Mode    RailMode // header (default production), tree, full
	Animate bool
	Frame   int
	ASCII   bool
	Plain   bool
}

// RailOpts controls environment-sensitive chrome.
type RailOpts struct {
	ASCII bool
	Color bool
}

// ChromeRenderOpts mirrors tool render env (NO_COLOR, TERM=dumb). Mirrors
// internal/legacytui's terminalToolRenderOptions (private to that package).
func ChromeRenderOpts() RailOpts {
	term := strings.ToLower(os.Getenv("TERM"))
	plain := os.Getenv("NO_COLOR") != "" || term == "dumb"
	return RailOpts{ASCII: term == "dumb", Color: !plain}
}

// railForBlock is a kind-level convenience (no group membership / no live view).
// Prefer resolveBlockRail for production.
func railForBlock(kind ChatBlockKind, toolFailed bool, opts RailOpts) LeftRail {
	b := ChatBlock{Kind: kind}
	if toolFailed {
		b.Text = "error: failed"
	}
	return ResolveBlockRail(b, GroupMember{}, opts, RailView{})
}

// railForChatBlock selects chrome without group context.
func railForChatBlock(block ChatBlock, opts RailOpts) LeftRail {
	return ResolveBlockRail(block, GroupMember{}, opts, RailView{})
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

// ApplyLeftRail paints the accent by rail.Mode.
// Blank / pad-only lines never get a glyph - empty rail column keeps alignment.
//
//   - Header: first non-blank line only
//   - Tree: first non-blank Glyph, later non-blank Char
//   - Full: every non-blank line Glyph (assistant speech)
//
// Shared with the classic-mode block renderer and work-group grouping in
// internal/clichat.
func ApplyLeftRail(lines []string, rail LeftRail) []string {
	if rail.Width == 0 || len(lines) == 0 {
		return lines
	}
	mode := rail.Mode
	if mode == RailModeOff {
		mode = RailModeFull // legacy Width>0 without Mode
	}
	primary := paintRailCell(rail)
	contRail := rail
	if rail.Char != "" {
		contRail.Glyph = rail.Char
	}
	contRail.Bold = false
	contRail.Animate = false
	cont := paintRailCell(contRail)
	if mode == RailModeHeader {
		cont = " "
	}

	body := make([]string, len(lines))
	railCol := make([]string, len(lines))
	blank := make([]bool, len(lines))
	firstContent := -1
	for i, line := range lines {
		plain := strings.TrimSpace(stripANSI(line))
		blank[i] = plain == ""
		if line == "" {
			body[i] = ""
		} else {
			body[i] = consumeFirstDisplaySpace(line)
		}
		if firstContent < 0 && !blank[i] {
			firstContent = i
		}
	}
	if firstContent < 0 {
		firstContent = 0
	}
	for i := range lines {
		// Pad / empty lanes: never paint glyph (user request: rail only on text).
		if blank[i] {
			railCol[i] = " "
			continue
		}
		switch mode {
		case RailModeFull:
			railCol[i] = primary
		case RailModeTree:
			if i == firstContent {
				railCol[i] = primary
			} else if i > firstContent {
				railCol[i] = cont
			} else {
				railCol[i] = " "
			}
		default: // Header
			if i == firstContent {
				railCol[i] = primary
			} else {
				railCol[i] = " "
			}
		}
	}
	joined := lipgloss.JoinHorizontal(
		lipgloss.Top,
		strings.Join(railCol, "\n"),
		strings.Join(body, "\n"),
	)
	out := strings.Split(joined, "\n")
	if len(out) < len(lines) {
		return applyLeftRailInject(lines, primary)
	}
	if len(out) > len(lines) {
		out = out[:len(lines)]
	}
	return out
}

// applyLeftRailInject is the line-by-line fallback when JoinHorizontal
// cannot preserve line count (should be rare).
func applyLeftRailInject(lines []string, cell string) []string {
	out := make([]string, len(lines))
	for i, line := range lines {
		if line == "" || strings.TrimSpace(stripANSI(line)) == "" {
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

// applyLeftRailHeader forces header-only mode.
func applyLeftRailHeader(lines []string, rail LeftRail) []string {
	rail.Mode = RailModeHeader
	return ApplyLeftRail(lines, rail)
}

// applyBlockChrome applies hierarchical state-aware rail chrome.
func applyBlockChrome(lines []string, block ChatBlock, text string, opts RailOpts) []string {
	return ApplyBlockChromeWith(lines, block, text, opts, GroupMember{}, RailView{})
}

// ApplyBlockChromeWith applies hierarchical state-aware rail chrome for a
// block inside a work group. Shared with the classic-mode block renderer in
// internal/clichat.
func ApplyBlockChromeWith(lines []string, block ChatBlock, text string, opts RailOpts, mem GroupMember, view RailView) []string {
	if len(lines) == 0 {
		return lines
	}
	if block.Kind == ChatBlockSystem &&
		strings.HasPrefix(strings.TrimSpace(text), "→") &&
		!block.Collapsed {
		return lines
	}
	return ApplyLeftRail(lines, ResolveBlockRail(block, mem, opts, view))
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

// consumeFirstDisplaySpace removes one leading display space (plain or after
// CSI) so a joined rail cell stays width-neutral. Lines without a leading
// space are returned unchanged (JoinHorizontal then grows width by 1).
func consumeFirstDisplaySpace(line string) string {
	if line == "" {
		return line
	}
	if strings.HasPrefix(line, " ") {
		return line[1:]
	}
	// ANSI-aware: drop first display space only.
	var b strings.Builder
	b.Grow(len(line))
	i := 0
	for i < len(line) {
		if line[i] == '\033' {
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
				j++
			}
			b.WriteString(line[i:j])
			i = j
			continue
		}
		if line[i] == ' ' {
			b.WriteString(line[i+1:])
			return b.String()
		}
		// No leading space - keep original.
		return line
	}
	return line
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
	// Line was only CSI / empty - still paint rail for full-height continuity.
	if b.Len() == 0 {
		return cell
	}
	return cell + b.String()
}

// Presets - neutral default; semantic only on error/running.
func RailUser() LeftRail {
	return LeftRail{Width: 0, Mode: RailModeOff}
}

func RailAssistant() LeftRail {
	return LeftRail{Width: 1, Glyph: "│", Char: "│", Color: ChromeNeutral, Mode: RailModeFull, Bold: false}
}

func RailThinking() LeftRail {
	return LeftRail{Width: 1, Glyph: "┊", Char: "┊", Color: ChromeNeutral, Mode: RailModeHeader}
}

func RailTools() LeftRail {
	return LeftRail{Width: 1, Glyph: "│", Char: "│", Color: ChromeNeutral, Mode: RailModeHeader}
}

func RailError() LeftRail {
	return LeftRail{Width: 1, Glyph: "!", Char: "!", Color: ChromeError, Bold: true, Mode: RailModeHeader}
}

// stripANSI is a package-local convenience alias of StripANSI.
var stripANSI = StripANSI

// StripANSI removes ANSI escape sequences from a string. Exported: relocated
// from internal/legacytui/bubble_leftrail.go, which is also called by three
// other internal/legacytui files (overlay.go, tui_selection.go, clipboard.go)
// that now reach it as StripANSI.
func StripANSI(s string) string {
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
