// Brand mark chrome for all TUI states (welcome + live work).
// Status/tool glyphs are hand-crafted single-cell braille (or ◇ idle),
// not slices of the large multi-line welcome diamond.
package cli

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/lipgloss"
)

// brandPhase is the high-level activity shown by the mark + chrome.
type brandPhase int

const (
	phaseIdle brandPhase = iota
	phaseWelcome
	phaseAwaiting // message sent, awaiting first response (brief)
	phaseThinking // model reasoning / waiting, no open tools
	phaseStreaming
	phaseTools
	phaseMulti
	phaseQueued
	phaseError
	phaseCancel
)

// Brand colors (256-color, engineer-readable).
const (
	brandColorIdle     = "15" // white — rest / identity
	brandColorWelcome  = "15"
	brandColorThinking = "14" // cyan
	brandColorStream   = "12" // blue
	brandColorTools    = "11" // yellow
	brandColorMulti    = "13" // magenta
	brandColorQueue    = "10" // green accent
	brandColorError    = "9"  // red
	brandColorCancel   = "8"  // dim
)

// brandWorkFrames is an 8-frame single-rune braille diamond pulse.
// Each frame is a complete small mark in one cell (2×4 dots), not the tip
// of a multi-line raster diamond.
//
// Braille bit map (Unicode):
//
//	1 4
//	2 5
//	3 6
//	7 8
//
// Geometry: filled L1 diamond in the cell, expanding/contracting.
// Rune values are fixed literals so the glyph set is reviewable.
var brandWorkFrames = []string{
	"⠶", // U+2836 dots 2,3,5,6     — inner diamond
	"⠛", // U+281B dots 1,2,4,5     — upper weight
	"⠿", // U+283F dots 1–6         — mid expand
	"⣿", // U+28FF all 8            — full pulse
	"⣶", // U+28F6 dots 2,3,5,6,7,8 — lower weight
	"⠿", // mid
	"⠛", // upper
	"⠶", // inner
}

func brandColor(p brandPhase) string {
	switch p {
	case phaseWelcome:
		return brandColorWelcome
	case phaseAwaiting:
		return brandColorThinking // use cyan, same as thinking
	case phaseThinking:
		return brandColorThinking
	case phaseStreaming:
		return brandColorStream
	case phaseTools:
		return brandColorTools
	case phaseMulti:
		return brandColorMulti
	case phaseQueued:
		return brandColorQueue
	case phaseError:
		return brandColorError
	case phaseCancel:
		return brandColorCancel
	default:
		return brandColorIdle
	}
}

func brandLabel(p brandPhase) string {
	switch p {
	case phaseWelcome:
		return "welcome"
	case phaseAwaiting:
		return "awaiting"
	case phaseThinking:
		return "thinking"
	case phaseStreaming:
		return "streaming"
	case phaseTools:
		return "tools"
	case phaseMulti:
		return "parallel"
	case phaseQueued:
		return "queued"
	case phaseError:
		return "error"
	case phaseCancel:
		return "cancelled"
	default:
		return "ready"
	}
}

// brandGlyph returns a single-cell status/tool glyph, phase-colored.
// Idle/static callers pass frame 0 with phaseIdle (or any non-working phase
// when they want the static diamond). Working phases cycle brandWorkFrames.
func brandGlyph(frame int, color string) string {
	if frame < 0 {
		frame = 0
	}
	ch := brandWorkFrames[frame%len(brandWorkFrames)]
	return lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render(ch)
}

// ─── Status header brand ──────────────────────────────────────────────
//
// The animated state diamond (stateLogoMiniRows) is the single brand and
// state carrier in the chat header; the wordmark is plain text beside it.

// brandNameStyled is the plain-text brand next to the diamond.
func brandNameStyled() string {
	return lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Bold(true).Render("mivia")
}

// fillStatusLine joins left and right on one physical line, padding the gap
// with a faint rule (rule=true) or plain spaces, clamping when too narrow.
func fillStatusLine(left, right string, width int, rule bool) string {
	if width <= 0 {
		return left + " " + right
	}
	lw, rw := lipgloss.Width(left), lipgloss.Width(right)
	spacerN := width - lw - rw
	if spacerN < 1 {
		return lipgloss.NewStyle().MaxWidth(width).Render(left + " " + right)
	}
	fill := strings.Repeat(" ", spacerN)
	if rule {
		fill = tuiHeaderStyle.Render(strings.Repeat("─", spacerN))
	}
	return left + fill + right
}

// deriveBrandPhase maps live TUI facts → brand phase.
func deriveBrandPhase(waiting bool, openTools int, streamLen int, queueLen int, hadError bool, elapsed time.Duration) brandPhase {
	if hadError && !waiting {
		return phaseError
	}
	if !waiting {
		if queueLen > 0 {
			return phaseQueued
		}
		return phaseIdle
	}
	if openTools >= 2 {
		return phaseMulti
	}
	if openTools == 1 {
		return phaseTools
	}
	if streamLen > 0 {
		return phaseStreaming
	}
	// No data yet — brief "awaiting" state before "thinking".
	// Use elapsed to differentiate: first ~2s is awaiting response from server,
	// after that the model is thinking/reasoning.
	if elapsed < 2*time.Second {
		return phaseAwaiting
	}
	return phaseThinking
}

// renderWorkChrome builds the two-line status header while the agent is active.
//
//	line 1: diamond row · mivia · model ─── phase · elapsed
//	line 2: diamond row · step detail        tools · queue
//
// The animated state diamond spans both lines and is present in every phase.
func renderWorkChrome(
	frame int,
	phase brandPhase,
	modelName string,
	elapsed time.Duration,
	openTools int,
	doneTools int,
	totalTools int,
	queueLen int,
	width int,
	stepDetail string,
) string {
	color := brandColor(phase)
	mini := stateLogoMiniRows(phase, frame)

	left1 := mini[0] + " " + brandNameStyled() + " " + tuiDimStyle.Render(modelName) + " "
	right1 := " " + lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Bold(true).Render(brandLabel(phase)) +
		tuiDimStyle.Render(" · "+formatDuration(elapsed)) + " "
	line1 := fillStatusLine(left1, right1, width, true)

	var tail []string
	switch phase {
	case phaseMulti, phaseTools:
		prog := fmt.Sprintf("%d active", openTools)
		if totalTools > 0 {
			prog = fmt.Sprintf("%d/%d tools", doneTools, totalTools)
		}
		tail = append(tail, lipgloss.NewStyle().Foreground(lipgloss.Color(brandColorTools)).Render(prog))
	case phaseStreaming:
		tail = append(tail, tuiDimStyle.Render("tokens"))
	}
	if queueLen > 0 {
		tail = append(tail, lipgloss.NewStyle().Foreground(lipgloss.Color(brandColorQueue)).Render(
			fmt.Sprintf("▣%d", queueLen),
		))
	}
	right2 := strings.Join(tail, tuiDimStyle.Render(" · "))
	if right2 != "" {
		right2 += " "
	}

	// Detail rides line 2, truncated before it can push tool/queue state off.
	detail := sanitizeStatusDetail(stepDetail)
	left2 := mini[1] + " "
	if detail != "" && width > 0 {
		available := width - lipgloss.Width(left2) - lipgloss.Width(right2) - 2
		if available < 1 {
			detail = ""
		} else {
			detail = truncateToWidth(detail, available)
		}
	}
	if detail != "" {
		left2 += tuiDimStyle.Render(detail)
	}
	line2 := fillStatusLine(left2, right2, width, false)

	return line1 + "\n" + line2
}

func sanitizeStatusDetail(detail string) string {
	detail = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, detail)
	return strings.Join(strings.Fields(detail), " ")
}

// countTools tallies open/done from rows.
func countTools(rows []toolRow) (open, done, total int) {
	total = len(rows)
	for _, r := range rows {
		if r.Done {
			done++
		} else {
			open++
		}
	}
	return open, done, total
}

// nanoFirstLine — tool-row leading glyph (single cell, phase-colored).
func nanoFirstLine(frame int, color string) string {
	return brandGlyph(frame, color)
}

// renderStatusBar is the sticky two-line chrome (idle + working). The state
// diamond leads both lines in every phase — it never leaves the screen.
func renderStatusBar(
	frame int,
	phase brandPhase,
	modelName string,
	waiting bool,
	elapsed time.Duration,
	openTools, doneTools, totalTools int,
	queueLen int,
	msgCount int,
	width int,
	stepDetail string,
) string {
	if waiting {
		return renderWorkChrome(frame, phase, modelName, elapsed, openTools, doneTools, totalTools, queueLen, width, stepDetail)
	}
	// Idle: the diamond keeps its slow clockwork breath.
	mini := stateLogoMiniRows(phase, frame)
	left1 := mini[0] + " " + brandNameStyled() + " " + tuiDimStyle.Render(modelName) + " "
	right1 := tuiDimStyle.Render(fmt.Sprintf(" %d msgs · /help ", msgCount))
	if queueLen > 0 {
		right1 = lipgloss.NewStyle().Foreground(lipgloss.Color(brandColorQueue)).Render(
			fmt.Sprintf(" ▣ %d ", queueLen),
		) + right1
	}
	line1 := fillStatusLine(left1, right1, width, true)
	return line1 + "\n" + mini[1]
}

// tryLoadHistoryNearTop is true when older session history can be prepended.
func tryLoadHistoryNearTop(msgOffset, yOffset int) bool {
	return msgOffset > 0 && yOffset < 3
}
