package transcript

import (
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// Header is a block's first row, in four columns.
// wireframes-panes.md section 2: label at column 1, then the detail,
// then a right-aligned meta and a right-aligned state. The four-column
// renderer itself lands in a later wave; the value is separated now so
// the block model does not change again to accept it.
type Header struct {
	Label  string
	Detail string
	Meta   string
	State  string
	// Role colours the state word. The word is always present, so
	// meaning survives with colour removed.
	Role theme.Role
}

// Block is one addressable unit of transcript. It is a value, not a
// rendered string, so it can be re-rendered when it collapses, when it
// gains focus, or when its state advances from running to ok.
type Block struct {
	ID   string
	Kind uievent.Kind

	Header Header
	Body   []string

	// Collapsible marks a block whose body may be hidden. Assistant
	// prose is not collapsible: it has no header to collapse into.
	Collapsible bool
	Collapsed   bool
	Focused     bool

	// Prose renders with no header and no indent, at column 1. It is the
	// only content that reads as conversation rather than as tooling
	// (wireframes-panes.md section 2, last paragraph).
	Prose bool
}

// isEmpty reports a block with nothing to render.
func (b Block) isEmpty() bool {
	return b.Header.Label == "" && b.Header.Detail == "" &&
		b.Header.Meta == "" && b.Header.State == "" && len(b.Body) == 0
}

// Height is the rendered row count, which the eviction budget consumes.
func (b Block) Height() int {
	if b.Prose {
		return len(b.Body)
	}
	if b.Collapsed {
		return 1
	}
	return 1 + len(b.Body)
}

// defaultCollapsed reports the state a block first renders in.
// wireframes-panes.md section 5: open under the threshold, closed at or
// above it.
func defaultCollapsed(body []string) bool {
	return len(body) >= uikitconfig.CollapseThresholdLines
}

// Render draws the block. The header row is byte-identical whether the
// block is collapsed or expanded, apart from the collapse marker, so
// toggling never moves any other row (wireframes-panes.md section 5).
func (b Block) Render(t theme.Theme, tier theme.Tier, width int) string {
	if b.Prose {
		return strings.Join(b.Body, "\n")
	}

	var sb strings.Builder
	sb.WriteString(b.renderHeader(t, tier, width))
	if b.Collapsed {
		return sb.String()
	}
	indent := strings.Repeat(" ", uikitconfig.BodyIndent)
	for _, line := range b.Body {
		sb.WriteByte('\n')
		sb.WriteString(indent)
		sb.WriteString(line)
	}
	return sb.String()
}

// collapseMarker is the column-1 glyph: "v" open, ">" closed, blank for
// a block that cannot collapse. wireframes-panes.md sections 2 and 3.
func (b Block) collapseMarker() string {
	switch {
	case !b.Collapsible:
		return " "
	case b.Collapsed:
		return ">"
	default:
		return "v"
	}
}

func (b Block) renderHeader(t theme.Theme, tier theme.Tier, width int) string {
	// A focused header is drawn as one reverse-video run rather than as
	// styled segments. Reverse inherits the theme's own contrast, so it
	// stays legible under any palette, and it never nests conflicting
	// styles. The collapse marker remains, so focus is signalled by
	// shape as well as by colour (docs/design/ux-rules.md rules 6.3-6.4).
	if b.Focused {
		return render.Role(t, tier, theme.RoleFG).Reverse(true).
			Render(b.headerPlain())
	}

	left := b.collapseMarker() + " " + render.Role(t, tier, theme.RoleFGMuted).Render(b.Header.Label)
	if b.Header.Detail != "" {
		left += " " + render.Role(t, tier, theme.RoleFG).Render(b.Header.Detail)
	}

	var right string
	if b.Header.Meta != "" {
		right = render.Role(t, tier, theme.RoleFGSubtle).Render(b.Header.Meta)
	}
	if b.Header.State != "" {
		role := b.Header.Role
		if role == "" {
			role = theme.RoleFGMuted
		}
		if right != "" {
			right += "  "
		}
		right += render.Role(t, tier, role).Render(b.Header.State)
	}
	if right == "" {
		return left
	}
	return left + "  " + right
}

// headerPlain is the header with no styling, used for the focused run
// and for width measurement.
func (b Block) headerPlain() string {
	out := b.collapseMarker() + " " + b.Header.Label
	if b.Header.Detail != "" {
		out += " " + b.Header.Detail
	}
	if b.Header.Meta != "" {
		out += "  " + b.Header.Meta
	}
	if b.Header.State != "" {
		out += "  " + b.Header.State
	}
	return out
}
