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

// brandIdleMark is the static identity diamond (◇), phase-colored.
func brandIdleMark(color string) string {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render(brandIdleGlyph)
}

// ─── Nav-brand wordmark: compact braille MIVIA for the status bar ─────

// navMIVIAGlyphFull are the primary braille glyphs for each letter (fully lit).
var navMIVIAGlyphFull = [5]string{
	"⣿", // M — full block (all 8 dots)
	"⠇", // I — vertical pillar (dots 1,2,3)
	"⣶", // V — inverted chevron (dots 2,3,5,6,7,8)
	"⠇", // I — vertical pillar
	"⣀", // A — base bar (dots 7,8)
}

// navMIVIAGlyphDim are dim/skeleton variants — single dots that still show
// position but at low visual weight. Each letter gets a distinct dot so the
// dim pattern reads as a subtle diagonal.
var navMIVIAGlyphDim = [5]string{
	"⡀", // M dim — dot 1 (top-left)
	"⠂", // I dim — dot 2 (mid-left)
	"⠄", // V dim — dot 3 (bottom-left)
	"⠈", // I dim — dot 4 (top-right)
	"⠐", // A dim — dot 5 (mid-right)
}

// navAnimFrame is a 5-letter mask: true = show full glyph, false = show dim.
type navAnimMask [5]bool

// navAnimPattern defines a named animation cycle: frame masks + speed divisor.
// speedDiv: how many raw logoFrames per pattern step (higher = slower).
type navAnimPattern struct {
	masks    []navAnimMask
	speedDiv int
}

// navAnims maps each active brand phase to a distinct animation pattern.
// Idle/welcome/queued are static (single-frame patterns).
var navAnims = map[brandPhase]navAnimPattern{
	// Static — no animation, all letters fully lit.
	phaseIdle:    {masks: []navAnimMask{{true, true, true, true, true}}, speedDiv: 1},
	phaseWelcome: {masks: []navAnimMask{{true, true, true, true, true}}, speedDiv: 1},
	phaseQueued:  {masks: []navAnimMask{{true, true, true, true, true}}, speedDiv: 1},
	phaseError:   {masks: []navAnimMask{{true, true, true, true, true}}, speedDiv: 1},
	phaseCancel:  {masks: []navAnimMask{{true, true, true, true, true}}, speedDiv: 1},

	// Awaiting — slow pulse, center letter only, to indicate "sent, waiting"
	phaseAwaiting: {masks: []navAnimMask{
		{false, false, true, false, false}, // V only
		{true, false, true, false, true},   // M, V, A
		{false, false, true, false, false}, // V only
		{false, true, true, true, false},   // I V I
	}, speedDiv: 5}, // 5 × 80ms = 400ms per step → 1.6s cycle

	// Thinking — slow scanner: one letter lights up at a time, left→right.
	// Each step advances one position.
	phaseThinking: {masks: []navAnimMask{
		{true, false, false, false, false}, // M
		{false, true, false, false, false}, // I
		{false, false, true, false, false}, // V
		{false, false, false, true, false}, // I
		{false, false, false, false, true}, // A
	}, speedDiv: 6}, // 6 ticks × 80ms = 480ms per step → 2.4s cycle

	// Streaming — center ripple: V lights up, then ripples outward.
	phaseStreaming: {masks: []navAnimMask{
		{false, false, true, false, false}, // V only
		{false, true, true, true, false},   // I V I
		{true, true, true, true, true},     // all
		{false, true, true, true, false},   // I V I
	}, speedDiv: 4}, // 4 × 80ms = 320ms per step → 1.28s cycle

	// Tools — fast strobe: all bright, then all dim, alternating.
	phaseTools: {masks: []navAnimMask{
		{true, true, true, true, true},      // all on
		{false, false, false, false, false}, // all dim
	}, speedDiv: 2}, // 2 × 80ms = 160ms → rapid blink

	// Multi — alternating columns: odd positions, then even positions.
	phaseMulti: {masks: []navAnimMask{
		{true, false, true, false, true},  // M, V, A
		{false, true, false, true, false}, // I, I
	}, speedDiv: 3}, // 3 × 80ms = 240ms per step
}

// renderNavBrandWordmark renders the compact braille MIVIA wordmark for the status bar.
// Idle: static white. Active: phase-colored with per-phase animation patterns.
// The frame counter drives letter-level glyph changes so the wordmark visibly animates.
func renderNavBrandWordmark(frame int, phase brandPhase) string {
	pat, ok := navAnims[phase]
	if !ok {
		pat = navAnims[phaseIdle]
	}
	if len(pat.masks) == 0 {
		pat = navAnims[phaseIdle]
	}

	color := brandColor(phase)
	dimColor := color
	// Dim letters get a darker shade of the same hue.
	switch color {
	case "15": // white → light gray
		dimColor = "250"
	case "14": // cyan → dim cyan
		dimColor = "6"
	case "12": // blue → dim blue
		dimColor = "4"
	case "11": // yellow → dim yellow/brown
		dimColor = "3"
	case "13": // magenta → dim magenta
		dimColor = "5"
	case "10": // green → dim green
		dimColor = "2"
	case "9": // red → dim red
		dimColor = "1"
	case "8": // dim → even dimmer
		dimColor = "236"
	default:
		dimColor = "244"
	}

	step := frame / pat.speedDiv
	mask := pat.masks[step%len(pat.masks)]

	cells := make([]string, 5)
	for i := 0; i < 5; i++ {
		var style lipgloss.Style
		if mask[i] {
			style = lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Bold(true)
			cells[i] = style.Render(navMIVIAGlyphFull[i])
		} else {
			style = lipgloss.NewStyle().Foreground(lipgloss.Color(dimColor))
			cells[i] = style.Render(navMIVIAGlyphDim[i])
		}
	}
	return strings.Join(cells[:], "")
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
	stepDetail string,
) string {
	color := brandColor(phase)
	left := renderNavBrandWordmark(frame, phase) + " " + tuiDimStyle.Render(modelName)

	var rightParts []string
	rightParts = append(rightParts, lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Bold(true).Render(brandLabel(phase)))
	rightParts = append(rightParts, tuiDimStyle.Render(" · "+formatDuration(elapsed)))

	// Show heartbeat/progress info when subagents are running.
	if stepDetail != "" && phase != phaseThinking {
		rightParts = append(rightParts, " ", tuiDimStyle.Render(stepDetail))
	}

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
	return renderNavBrandWordmark(0, phaseIdle) + " " + tuiDimStyle.Render(modelName)
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
	stepDetail string,
) string {
	if waiting {
		return renderWorkChrome(frame, phase, modelName, elapsed, openTools, doneTools, totalTools, queueLen, width, stepDetail)
	}
	// Idle
	left := renderIdleStatusLeft(modelName)
	hint := "/help"
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
