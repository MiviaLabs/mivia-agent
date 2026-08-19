package transcript

import (
	"strings"

	"github.com/charmbracelet/x/ansi"

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

	// CallID ties every event of one tool call to one block, so pending
	// -> running -> ok/failed updates the same header in place instead of
	// stacking three blocks for one call.
	CallID string

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

// Height is the TERMINAL row count at width, which the eviction budget
// consumes. It is not the count of logical body lines: a line wider than
// the terminal draws on two rows or more, and counting it as one would
// make the budget nominal instead of real.
//
// Height and Render both derive their rows from bodyRows, so they cannot
// disagree.
//
// The trailing +1 is the blank separator row after the block
// (docs/design/wireframes.md variant A, mivia-ui-mock.html): every
// top-level block in the transcript is followed by one blank row, or
// adjacent blocks read as one dense, cramped run of text instead of
// distinct entries. It applies uniformly, collapsed or not, so spacing
// never depends on collapse state.
func (b Block) Height(width int) int {
	if b.Prose {
		return len(b.bodyRows(width)) + 1
	}
	if b.Collapsed {
		return 1 + 1
	}
	// render.Header guarantees exactly one row at a known width.
	return 1 + len(b.bodyRows(width)) + 1
}

// bodyRows is the body as terminal rows at width, already wrapped.
//
// Prose wraps to a reading measure on word boundaries. Tool output and
// code hard-wrap at the terminal edge: they are not prose, and a break
// on a word boundary would misrepresent the bytes the tool produced.
// Already-styled prose hard-wraps too, because the escape sequences in
// it belong to code that should not reflow.
func (b Block) bodyRows(width int) []string {
	if width <= 0 {
		return b.Body
	}
	if b.Prose {
		measure := render.ProseMeasure(width)
		out := make([]string, 0, len(b.Body))
		for _, line := range b.Body {
			if strings.Contains(line, "\x1b") {
				out = append(out, render.HardWrap(line, width)...)
				continue
			}
			out = append(out, render.Wrap(line, measure)...)
		}
		return out
	}
	inner := width - uikitconfig.BodyIndent
	out := make([]string, 0, len(b.Body))
	for _, line := range b.Body {
		out = append(out, render.HardWrap(line, inner)...)
	}
	return out
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
//
// The last row is always the blank separator Height accounts for; see
// its doc comment.
func (b Block) Render(t theme.Theme, tier theme.Tier, width int) string {
	if b.Prose {
		return strings.Join(b.bodyRows(width), "\n") + "\n"
	}

	var sb strings.Builder
	sb.WriteString(b.renderHeader(t, tier, width))
	if b.Collapsed {
		sb.WriteByte('\n')
		return sb.String()
	}
	indent := strings.Repeat(" ", uikitconfig.BodyIndent)
	for _, line := range b.bodyRows(width) {
		sb.WriteByte('\n')
		sb.WriteString(indent)
		sb.WriteString(line)
	}
	sb.WriteByte('\n')
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
		// Truncated to width for the same reason the unfocused header is:
		// a header that overflows draws two rows, and Height budgets one.
		plain := b.headerPlain()
		if width > 0 {
			plain = ansi.Truncate(plain, width, "")
		}
		return render.Role(t, tier, theme.RoleFG).Reverse(true).Render(plain)
	}

	return render.Header(t, tier, width, render.HeaderSpec{
		Marker:    b.collapseMarker(),
		Label:     b.Header.Label,
		Detail:    b.Header.Detail,
		Meta:      b.Header.Meta,
		State:     b.Header.State,
		StateRole: b.Header.Role,
	})
}

// headerPlain is the header with no styling, used for the focused run
// and for width measurement.
func (b Block) headerPlain() string {
	spec := render.SanitizeSpec(render.HeaderSpec{
		Marker: b.collapseMarker(),
		Label:  b.Header.Label,
		Detail: b.Header.Detail,
		Meta:   b.Header.Meta,
		State:  b.Header.State,
	})
	out := spec.Marker + " " + spec.Label
	if spec.Detail != "" {
		out += " " + spec.Detail
	}
	if spec.Meta != "" {
		out += "  " + spec.Meta
	}
	if spec.State != "" {
		out += "  " + spec.State
	}
	return out
}
