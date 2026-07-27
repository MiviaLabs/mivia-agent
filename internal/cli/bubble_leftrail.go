package cli

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Chrome color tokens — semantic status only (not tool names).
// Tools/steps default to neutral gray; yellow is for status-bar tools phase only.
const (
	chromeNeutral   = brandColorCancel   // "8" dim gray — structure default
	chromeUser      = brandColorStream   // "12" blue — user label (not rail)
	chromeAssistant = brandColorCancel   // "8" quiet speech
	chromeThinking  = brandColorMulti    // "13" magenta — rare multi
	chromeTools     = brandColorTools    // "11" yellow — brand bar phase only
	chromeOK        = brandColorQueue    // "10" green — rare; not default tool OK
	chromeError     = brandColorError    // "9" red — strict failures only
	chromeAwait     = brandColorThinking // "14" cyan — live running pulse
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

// railForBlock is a kind-level convenience (no group membership / no live view).
// Prefer resolveBlockRail for production.
func railForBlock(kind ChatBlockKind, toolFailed bool, opts railOpts) LeftRail {
	b := ChatBlock{Kind: kind}
	if toolFailed {
		b.Text = "error: failed"
	}
	return resolveBlockRail(b, groupMember{}, opts, railView{})
}

// railForChatBlock selects chrome without group context.
func railForChatBlock(block ChatBlock, opts railOpts) LeftRail {
	return resolveBlockRail(block, groupMember{}, opts, railView{})
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

// applyLeftRail paints the accent by rail.Mode:
//   - Header: first content line only (default production)
//   - Tree: first Glyph, later Char
//   - Full: every line (legacy callers with Mode unset + Width>0)
func applyLeftRail(lines []string, rail LeftRail) []string {
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
	firstContent := -1
	for i, line := range lines {
		if line == "" {
			body[i] = ""
		} else {
			body[i] = consumeFirstDisplaySpace(line)
		}
		if firstContent < 0 && strings.TrimSpace(stripANSI(line)) != "" {
			firstContent = i
		}
	}
	if firstContent < 0 {
		firstContent = 0
	}
	for i := range lines {
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
	return applyLeftRail(lines, rail)
}

// applyBlockChrome applies hierarchical state-aware rail chrome.
func applyBlockChrome(lines []string, block ChatBlock, text string, opts railOpts) []string {
	return applyBlockChromeWith(lines, block, text, opts, groupMember{}, railView{})
}

func applyBlockChromeWith(lines []string, block ChatBlock, text string, opts railOpts, mem groupMember, view railView) []string {
	if len(lines) == 0 {
		return lines
	}
	if block.Kind == ChatBlockSystem &&
		strings.HasPrefix(strings.TrimSpace(text), "→") &&
		!block.Collapsed {
		return lines
	}
	return applyLeftRail(lines, resolveBlockRail(block, mem, opts, view))
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
		// No leading space — keep original.
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
	// Line was only CSI / empty — still paint rail for full-height continuity.
	if b.Len() == 0 {
		return cell
	}
	return cell + b.String()
}

// blockToolFailed is strict — no false red when body mentions "error" mid-text.
//
// True when any of:
//   - ChatBlock.Failed (production toolRow.Failed)
//   - body/rendered has exit=1|error|timeout|canceled as a token (not exit=10)
//   - first non-empty text line is error:/failed: / exact "failed"/"error"
func blockToolFailed(b ChatBlock) bool {
	if b.Kind != ChatBlockTool {
		return false
	}
	if b.Failed {
		return true
	}
	if hasExitFailureToken(b.Text) || hasExitFailureToken(b.Rendered) {
		return true
	}
	first := strings.ToLower(firstNonEmptyLine(b.Text))
	if first == "error" || first == "failed" ||
		strings.HasPrefix(first, "error:") ||
		strings.HasPrefix(first, "failed:") {
		return true
	}
	return false
}

// hasExitFailureToken finds exit=<code> without matching exit=10 as exit=1.
func hasExitFailureToken(s string) bool {
	low := strings.ToLower(s)
	const prefix = "exit="
	for idx := 0; idx < len(low); {
		i := strings.Index(low[idx:], prefix)
		if i < 0 {
			return false
		}
		i += idx
		rest := low[i+len(prefix):]
		switch {
		case strings.HasPrefix(rest, "error"),
			strings.HasPrefix(rest, "timeout"),
			strings.HasPrefix(rest, "canceled"),
			strings.HasPrefix(rest, "cancelled"):
			return true
		case strings.HasPrefix(rest, "1"):
			// exit=1 only — not exit=10, exit=12, …
			if len(rest) == 1 || rest[1] < '0' || rest[1] > '9' {
				return true
			}
		}
		idx = i + len(prefix)
	}
	return false
}

// Presets — neutral default; semantic only on error/running.
func RailUser() LeftRail {
	return LeftRail{Width: 0, Mode: RailModeOff}
}

func RailAssistant() LeftRail {
	return LeftRail{Width: 1, Glyph: "│", Char: " ", Color: chromeNeutral, Mode: RailModeHeader}
}

func RailThinking() LeftRail {
	return LeftRail{Width: 1, Glyph: "┊", Char: "┊", Color: chromeNeutral, Mode: RailModeHeader}
}

func RailTools() LeftRail {
	return LeftRail{Width: 1, Glyph: "│", Char: "│", Color: chromeNeutral, Mode: RailModeHeader}
}

func RailError() LeftRail {
	return LeftRail{Width: 1, Glyph: "!", Char: "!", Color: chromeError, Bold: true, Mode: RailModeHeader}
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
