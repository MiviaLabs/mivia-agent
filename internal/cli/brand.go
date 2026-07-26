// Brand mark chrome for all TUI states (welcome + live work).
// Uses the braille diamond pixel engine; color encodes phase.
package cli

import (
	"fmt"
	"strings"
	"sync"
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

var (
	miniOnce   sync.Once
	miniFrames []string // ~8×4 braille cells — status / work rail
	nanoFrames []string // ~4×3 cells — tool row leading glyph strip
)

const (
	miniPixelW = 24
	miniPixelH = 24
	nanoPixelW = 12
	nanoPixelH = 12
)

func ensureMiniFrames() {
	miniOnce.Do(func() {
		miniFrames = diamondAnimFrames(miniPixelW, miniPixelH, logoNFrames)
		nanoFrames = diamondAnimFrames(nanoPixelW, nanoPixelH, logoNFrames)
	})
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

// renderMiniMark returns a compact multi-line braille diamond for the work rail.
func renderMiniMark(frame int, color string) string {
	ensureMiniFrames()
	if len(miniFrames) == 0 {
		return ""
	}
	if frame < 0 {
		frame = 0
	}
	return styleBrailleFrame(miniFrames[frame%len(miniFrames)], 0, color)
}

// renderNanoMark is a tiny mark for embedding beside tool rows / status.
func renderNanoMark(frame int, color string) string {
	ensureMiniFrames()
	if len(nanoFrames) == 0 {
		return ""
	}
	if frame < 0 {
		frame = 0
	}
	return styleBrailleFrame(nanoFrames[frame%len(nanoFrames)], 0, color)
}

// nanoFirstLine is a single-line glyph strip for tool-row leading icons.
func nanoFirstLine(frame int, color string) string {
	art := renderNanoMark(frame, color)
	if art == "" {
		return lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render("◇")
	}
	line := strings.Split(art, "\n")[0]
	return strings.TrimRight(line, " ")
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

// renderWorkChrome builds a single-line status bar while the agent is active.
// Tiny animated nano mark (not the large diamond) + phase label + meta.
// Full-size braille diamond stays on the welcome screen only.
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
	// Single braille strip — subtle, not a multi-row hero mark.
	mark := nanoFirstLine(frame, color)
	label := lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Bold(true).Render(brandLabel(phase))

	var parts []string
	parts = append(parts, mark, " ", label)
	parts = append(parts, tuiDimStyle.Render(" · "+formatDuration(elapsed)))
	parts = append(parts, tuiDimStyle.Render(" · "+modelName))

	switch phase {
	case phaseMulti, phaseTools:
		prog := fmt.Sprintf("%d active", openTools)
		if totalTools > 0 {
			prog = fmt.Sprintf("%d/%d tools", doneTools, totalTools)
		}
		parts = append(parts, " ", lipgloss.NewStyle().Foreground(lipgloss.Color(brandColorTools)).Render(prog))
	case phaseStreaming:
		parts = append(parts, tuiDimStyle.Render(" · tokens"))
	}
	if queueLen > 0 {
		parts = append(parts, " ", lipgloss.NewStyle().Foreground(lipgloss.Color(brandColorQueue)).Render(
			fmt.Sprintf("▣%d", queueLen),
		))
	}

	left := strings.Join(parts, "")
	// Pad with a faint rule like the idle status bar.
	if width > 0 {
		lw := lipgloss.Width(left)
		spacerN := width - lw
		if spacerN < 1 {
			spacerN = 1
		}
		// Trim left if somehow wider than terminal.
		if lw >= width {
			return left
		}
		return left + tuiHeaderStyle.Render(strings.Repeat("─", spacerN))
	}
	return left
}

// renderIdleStatusLeft is a tiny static brand for the idle status bar.
func renderIdleStatusLeft(modelName string) string {
	ensureMiniFrames()
	// Static brand frame 0, single first line nano, white.
	g := nanoFirstLine(0, brandColorIdle)
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
