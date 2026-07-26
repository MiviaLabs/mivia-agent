// Brand mark chrome for all TUI states (welcome + live work).
// Status/tool glyphs are hand-crafted single-cell braille (or ◇ idle),
// not slices of the large multi-line welcome diamond.
package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// brandPhase is the high-level activity shown by the mark + chrome.
type brandPhase int

const (
	phaseIdle brandPhase = iota
	phaseWelcome
	phaseThinking  // model reasoning / waiting, no open tools
	phaseStreaming // assistant tokens flowing
	phaseTools     // ≥1 tool running
	phaseMulti     // ≥2 tools
	phaseQueued    // has queue (layer; color accent)
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

// brandIdleGlyph is the static 1-cell identity mark (not a raster slice).
const brandIdleGlyph = "◇" // U+25C7 WHITE DIAMOND

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

// brandIdleMark is the static identity diamond (◇), phase-colored.
func brandIdleMark(color string) string {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render(brandIdleGlyph)
}

// deriveBrandPhase maps live TUI facts → brand phase.
func deriveBrandPhase(waiting bool, openTools int, streamLen int, queueLen int, hadError bool) brandPhase {
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
	return phaseThinking
}

// renderWorkChrome builds a single physical status line while the agent is active.
//
//	left:  glyph + mivia + model
//	right: phase · elapsed · tools[/queue]
//	middle: faint rule filling the gap
//
// Large multi-line braille diamond stays on the welcome screen only.
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
) string {
	color := brandColor(phase)
	glyph := statusGlyph(frame, phase)
	left := glyph + tuiAccentStyle.Render(" mivia ") + tuiDimStyle.Render(modelName)

	var rightParts []string
	rightParts = append(rightParts, lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Bold(true).Render(brandLabel(phase)))
	rightParts = append(rightParts, tuiDimStyle.Render(" · "+formatDuration(elapsed)))

	switch phase {
	case phaseMulti, phaseTools:
		prog := fmt.Sprintf("%d active", openTools)
		if totalTools > 0 {
			prog = fmt.Sprintf("%d/%d tools", doneTools, totalTools)
		}
		rightParts = append(rightParts, " ", lipgloss.NewStyle().Foreground(lipgloss.Color(brandColorTools)).Render(prog))
	case phaseStreaming:
		rightParts = append(rightParts, tuiDimStyle.Render(" · tokens"))
	}
	if queueLen > 0 {
		rightParts = append(rightParts, " ", lipgloss.NewStyle().Foreground(lipgloss.Color(brandColorQueue)).Render(
			fmt.Sprintf("▣%d", queueLen),
		))
	}
	right := strings.Join(rightParts, "")

	if width <= 0 {
		return left + " " + right
	}
	lw, rw := lipgloss.Width(left), lipgloss.Width(right)
	spacerN := width - lw - rw
	if spacerN < 1 {
		// Prefer full left identity; trim right if terminal is narrow.
		if lw+1 >= width {
			return left
		}
		return left + " " + right
	}
	return left + tuiHeaderStyle.Render(strings.Repeat("─", spacerN)) + right
}

// renderIdleStatusLeft is a static brand for the idle status bar.
func renderIdleStatusLeft(modelName string) string {
	g := brandIdleMark(brandColorIdle)
	return g + tuiAccentStyle.Render(" mivia ") + tuiDimStyle.Render(modelName)
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

// statusGlyph is the status-bar leading mark for a phase.
func statusGlyph(frame int, phase brandPhase) string {
	switch phase {
	case phaseIdle, phaseWelcome:
		return brandIdleMark(brandColor(phase))
	case phaseError:
		return lipgloss.NewStyle().Foreground(lipgloss.Color(brandColorError)).Bold(true).Render("✗")
	case phaseCancel:
		return lipgloss.NewStyle().Foreground(lipgloss.Color(brandColorCancel)).Render("–")
	default:
		return brandGlyph(frame, brandColor(phase))
	}
}

// renderStatusBar is the sticky one-line chrome (idle + working).
// Grok-style: left identity, right phase · time · tools, mid ─.
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
	showThinking bool,
) string {
	if waiting {
		return renderWorkChrome(frame, phase, modelName, elapsed, openTools, doneTools, totalTools, queueLen, width)
	}
	// Idle
	left := renderIdleStatusLeft(modelName)
	hint := "/help"
	if showThinking {
		hint = "thinking on"
	}
	right := tuiDimStyle.Render(fmt.Sprintf(" %d msgs · %s ", msgCount, hint))
	if queueLen > 0 {
		right = lipgloss.NewStyle().Foreground(lipgloss.Color(brandColorQueue)).Render(
			fmt.Sprintf(" ▣ %d ", queueLen),
		) + right
	}
	if width <= 0 {
		return left + right
	}
	lw, rw := lipgloss.Width(left), lipgloss.Width(right)
	spacerN := width - lw - rw
	if spacerN < 1 {
		return left
	}
	return left + tuiHeaderStyle.Render(strings.Repeat("─", spacerN)) + right
}

// tryLoadHistoryNearTop is true when older session history can be prepended.
func tryLoadHistoryNearTop(msgOffset, yOffset int) bool {
	return msgOffset > 0 && yOffset < 3
}
