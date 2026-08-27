package clichat

import "strings"

// chromeAwait is the live-running-pulse rail color (relocated from
// bubble_leftrail.go: its only caller is this file).
const chromeAwait = BrandColorThinking // "44" vivid cyan - live running pulse

// Hierarchical left-rail roles + semantic color palette.
//
// Challenged design (why not yellow/red per tool name):
//   - Color encodes lifecycle state only (running / failed / live multi).
//   - Structure (group header vs step) uses glyph weight, not hue.
//   - read_file / run_command / kill / delegate share the same thin gray rail.
//   - Red only on strict failure (never body text containing "error").
//   - Animate only while Live; history freezes.
//
// Matrix:
//
//	| What              | Glyph     | Weight | Mode   | Color   | Animate |
//	| User              | (off)     | -      | off    | bg bar  | no      |
//	| Assistant         | │         | thin   | header | neutral | no      |
//	| Thinking history  | ┊         | thin   | header | neutral | no      |
//	| Thinking live     | pulse     | thin   | header | cyan    | yes     |
//	| Tool any name OK  | │         | thin   | header | neutral | no      |
//	| Tool first-in-grp | ├         | med    | header | neutral | no      |
//	| Group header      | ┃         | heavy  | header | neutral | no*     |
//	| Group live multi  | pulse     | heavy  | header | cyan    | yes     |
//	| Tool failed       | !         | bold   | header | red     | no      |
//
// * live multi elevates group header to cyan pulse.

// RailMode controls vertical paint of the accent.
type RailMode int

const (
	RailModeOff RailMode = iota
	RailModeHeader
	RailModeTree
	RailModeFull
)

// RailRole is structural place in the timeline / work group.
type RailRole int

const (
	RailRoleNone RailRole = iota
	RailRoleGroupHeader
	RailRoleFirstStep
	RailRoleStep
	RailRoleStandalone
	RailRoleThinking
	RailRoleAssistant
)

// RailState is lifecycle color (never tool identity).
type RailState int

const (
	RailStateNeutral RailState = iota
	RailStateRunning
	RailStateFailed
	RailStateParallelLive
)

// RailView is per-frame view context (piggybacks logoFrame).
type RailView struct {
	Frame int
	Live  bool
}

// GroupMember describes a block's place inside a multi-tool work group.
type GroupMember struct {
	InGroup   bool
	ToolIndex int // 0-based among tools; -1 if not a tool
	ToolCount int
	GroupKey  string
	IsHeader  bool
}

func buildGroupMembers(blocks []ChatBlock) []GroupMember {
	out := make([]GroupMember, len(blocks))
	for _, g := range FindWorkGroups(blocks) {
		toolI := 0
		for i := g.Start; i < g.End && i < len(blocks); i++ {
			m := GroupMember{
				InGroup:   true,
				ToolIndex: -1,
				ToolCount: g.ToolCount,
				GroupKey:  g.Key,
			}
			if blocks[i].Kind == ChatBlockTool {
				m.ToolIndex = toolI
				toolI++
			}
			out[i] = m
		}
	}
	return out
}

func headerGroupMember(toolCount int, key string) GroupMember {
	return GroupMember{
		InGroup:   true,
		ToolIndex: -1,
		ToolCount: toolCount,
		GroupKey:  key,
		IsHeader:  true,
	}
}

func resolveRailRole(block ChatBlock, mem GroupMember) RailRole {
	if mem.IsHeader {
		if mem.ToolCount >= 2 {
			return RailRoleGroupHeader
		}
		return RailRoleNone
	}
	switch block.Kind {
	// The assistant is the default voice of the transcript, so it carries no
	// rail: a marker in front of nearly every line marks the rule instead of
	// the exception and leaves the screen striped with bars. Model prose sits
	// at the margin; you (▌) and work (structural rails) are what stand out.
	case ChatBlockUser, ChatBlockSystem, ChatBlockDivider, ChatBlockAssistant:
		return RailRoleNone
	case ChatBlockThinking:
		return RailRoleThinking
	case ChatBlockTool:
		if mem.InGroup {
			if mem.ToolIndex == 0 {
				return RailRoleFirstStep
			}
			return RailRoleStep
		}
		return RailRoleStandalone
	default:
		return RailRoleNone
	}
}

func resolveRailState(block ChatBlock, role RailRole, view RailView) RailState {
	if blockToolFailed(block) {
		return RailStateFailed
	}
	if view.Live && role == RailRoleGroupHeader {
		return RailStateParallelLive
	}
	if view.Live && block.Kind == ChatBlockThinking && !block.Collapsed {
		return RailStateRunning
	}
	// Tools default neutral - never yellow/green by name or "OK".
	return RailStateNeutral
}

// blockToolFailed is strict - no false red when body mentions "error"
// mid-text (relocated from bubble_leftrail.go: its only caller is this
// file).
//
// True when any of:
//   - ChatBlock.Failed (production ToolRow.Failed)
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
			// exit=1 only - not exit=10, exit=12, …
			if len(rest) == 1 || rest[1] < '0' || rest[1] > '9' {
				return true
			}
		}
		idx = i + len(prefix)
	}
	return false
}

// railFromRole: structure = glyph/weight; color = state only.
func railFromRole(role RailRole, state RailState, opts RailOpts, view RailView) LeftRail {
	if role == RailRoleNone {
		return LeftRail{Width: 0, Mode: RailModeOff}
	}
	r := LeftRail{
		Width: 1,
		ASCII: opts.ASCII,
		Plain: !opts.Color,
		Mode:  RailModeHeader,
		Frame: view.Frame,
		Color: ChromeNeutral,
		Bold:  false,
	}

	switch role {
	case RailRoleGroupHeader:
		r.Glyph, r.Char = "┃", "┃"
		r.Bold = true
		if opts.ASCII {
			r.Glyph, r.Char = "#", "#"
		}
	case RailRoleFirstStep:
		r.Glyph, r.Char = "├", "│"
		if opts.ASCII {
			r.Glyph, r.Char = "+", "|"
		}
	case RailRoleStep, RailRoleStandalone:
		r.Glyph, r.Char = "│", "│"
		if opts.ASCII {
			r.Glyph, r.Char = "|", "|"
		}
	case RailRoleThinking:
		r.Glyph, r.Char = "┊", "┊"
		if opts.ASCII {
			r.Glyph, r.Char = ":", ":"
		}
	case RailRoleAssistant:
		// Thin bar on every text line only (applyLeftRail skips blanks).
		// Never half-block ▌ - reads too heavy next to speech.
		r.Glyph, r.Char = "│", "│"
		r.Bold = false
		r.Mode = RailModeFull
		if opts.ASCII {
			r.Glyph, r.Char = "|", "|"
		}
	default:
		r.Width = 0
		return r
	}

	switch state {
	case RailStateFailed:
		r.Color = ChromeError
		r.Glyph, r.Char = "!", "!"
		r.Bold = true
		r.Animate = false
	case RailStateRunning:
		r.Color = chromeAwait
		r.Animate = view.Live
	case RailStateParallelLive:
		r.Color = chromeAwait
		r.Animate = view.Live
		r.Bold = true
	default:
		r.Color = ChromeNeutral
		r.Animate = false
	}

	if r.Animate && view.Live {
		if opts.ASCII {
			frames := []string{"*", "+", "x", "o"}
			r.Glyph = frames[view.Frame%len(frames)]
		} else if len(BrandWorkFrames) > 0 {
			r.Glyph = BrandWorkFrames[view.Frame%len(BrandWorkFrames)]
		}
	}
	return r
}

// ResolveBlockRail resolves the left-rail glyph and mode for a chat block.
// Shared with internal/legacytui's bubble-mode transcript layout.
func ResolveBlockRail(block ChatBlock, mem GroupMember, opts RailOpts, view RailView) LeftRail {
	if block.Kind == ChatBlockDivider {
		return railForDividerText(block.Text, opts)
	}
	role := resolveRailRole(block, mem)
	if role == RailRoleNone {
		return LeftRail{Width: 0, Mode: RailModeOff}
	}
	return railFromRole(role, resolveRailState(block, role, view), opts, view)
}

// railForDividerText enables error chrome when divider text looks like an
// error (relocated from bubble_leftrail.go: its only caller is this file).
func railForDividerText(text string, opts RailOpts) LeftRail {
	low := strings.ToLower(strings.TrimSpace(text))
	if strings.HasPrefix(low, "error") || strings.Contains(low, "error:") {
		r := LeftRail{Width: 1, Color: ChromeError, ASCII: opts.ASCII, Plain: !opts.Color, Bold: true}
		r.Glyph = "!"
		if opts.ASCII {
			r.Glyph = "!"
		}
		r.Char = r.Glyph
		return r
	}
	return LeftRail{Width: 0}
}

func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}
