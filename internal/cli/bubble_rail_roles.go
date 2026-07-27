package cli

import "strings"

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
//	| User              | (off)     | —      | off    | bg bar  | no      |
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

// railView is per-frame view context (piggybacks logoFrame).
type railView struct {
	Frame int
	Live  bool
}

// groupMember describes a block's place inside a multi-tool work group.
type groupMember struct {
	InGroup   bool
	ToolIndex int // 0-based among tools; -1 if not a tool
	ToolCount int
	GroupKey  string
	IsHeader  bool
}

func buildGroupMembers(blocks []ChatBlock) []groupMember {
	out := make([]groupMember, len(blocks))
	for _, g := range findWorkGroups(blocks) {
		toolI := 0
		for i := g.Start; i < g.End && i < len(blocks); i++ {
			m := groupMember{
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

func headerGroupMember(toolCount int, key string) groupMember {
	return groupMember{
		InGroup:   true,
		ToolIndex: -1,
		ToolCount: toolCount,
		GroupKey:  key,
		IsHeader:  true,
	}
}

func resolveRailRole(block ChatBlock, mem groupMember) RailRole {
	if mem.IsHeader {
		if mem.ToolCount >= 2 {
			return RailRoleGroupHeader
		}
		return RailRoleNone
	}
	switch block.Kind {
	case ChatBlockUser, ChatBlockSystem, ChatBlockDivider:
		return RailRoleNone
	case ChatBlockAssistant:
		return RailRoleAssistant
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

func resolveRailState(block ChatBlock, role RailRole, view railView) RailState {
	if blockToolFailed(block) {
		return RailStateFailed
	}
	if view.Live && role == RailRoleGroupHeader {
		return RailStateParallelLive
	}
	if view.Live && block.Kind == ChatBlockThinking && !block.Collapsed {
		return RailStateRunning
	}
	// Tools default neutral — never yellow/green by name or "OK".
	return RailStateNeutral
}

// railFromRole: structure = glyph/weight; color = state only.
func railFromRole(role RailRole, state RailState, opts railOpts, view railView) LeftRail {
	if role == RailRoleNone {
		return LeftRail{Width: 0, Mode: RailModeOff}
	}
	r := LeftRail{
		Width: 1,
		ASCII: opts.ASCII,
		Plain: !opts.Color,
		Mode:  RailModeHeader,
		Frame: view.Frame,
		Color: chromeNeutral,
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
		r.Glyph, r.Char = "│", " "
		if opts.ASCII {
			r.Glyph, r.Char = "|", " "
		}
	default:
		r.Width = 0
		return r
	}

	switch state {
	case RailStateFailed:
		r.Color = chromeError
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
		r.Color = chromeNeutral
		r.Animate = false
	}

	if r.Animate && view.Live {
		if opts.ASCII {
			frames := []string{"*", "+", "x", "o"}
			r.Glyph = frames[view.Frame%len(frames)]
		} else if len(brandWorkFrames) > 0 {
			r.Glyph = brandWorkFrames[view.Frame%len(brandWorkFrames)]
		}
	}
	return r
}

func resolveBlockRail(block ChatBlock, mem groupMember, opts railOpts, view railView) LeftRail {
	if block.Kind == ChatBlockDivider {
		return railForDividerText(block.Text, opts)
	}
	role := resolveRailRole(block, mem)
	if role == RailRoleNone {
		return LeftRail{Width: 0, Mode: RailModeOff}
	}
	return railFromRole(role, resolveRailState(block, role, view), opts, view)
}

func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}
