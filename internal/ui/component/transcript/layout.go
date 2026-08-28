package transcript

import (
	"path/filepath"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// layout turns the block list into terminal geometry: where each block
// starts, how many rows it owns, and the two presentation rules that
// belong to the SEQUENCE rather than to any single block
// (transcript-polish.md R1):
//
//   - separators: one blank row before a block that starts a new
//     section - anything after prose, or prose itself - and NO blank row
//     inside a run of activity blocks. Spacing follows turns, not events.
//   - group indent: activity blocks draw at a 2-column indent, the
//     marker at column 3, so a turn's tool activity reads as one group
//     hanging under the turn's prose.
//
// R2 adds the leader run: two or more consecutive collapsed read-only
// tool calls draw as ONE row ("Read 3 files: a.go, b.go") instead of
// three headers. Coalescing is display-only - the children stay real
// blocks, so focus, click-to-expand, copy, and Dump keep per-child
// identity and full content.

// groupIndent is the column count an activity block is shifted by.
const groupIndent = 2

type span struct {
	top       int  // first terminal row of the block, separators included
	height    int  // terminal rows the block owns (0 when hidden in a run)
	indent    int  // columns the block is shifted by (groupIndent when activity)
	sepBefore bool // a blank separator row sits directly above top
	// runSize is 0 for a normal block, or the number of collapsed
	// read-only children this block's leader row stands in for
	// (itself included). A run member that is not the head draws
	// nothing; its top points at the leader's row so focus and click
	// routing land on the row the user can see.
	runSize int
	runTop  int // row of the run's leader row (== top for the head)
}

// layout computes the span of every block at the current width.
func (m Model) layout() []span {
	spans := make([]span, len(m.blocks))
	row := 0
	prevActivity := false
	for i := 0; i < len(m.blocks); {
		b := m.blocks[i]
		act := b.Activity()
		ind := 0
		if act {
			ind = groupIndent
		}
		sp := span{top: row, indent: ind, sepBefore: i > 0 && !(prevActivity && act)}
		if sp.sepBefore {
			row++
		}
		if n := m.leaderRunLen(i); n > 0 {
			sp.height, sp.runSize, sp.runTop = 1, n, row
			spans[i] = sp
			row++
			for k := 1; k < n; k++ {
				spans[i+k] = span{top: row - 1, height: 0, indent: ind, runSize: n, runTop: row - 1}
			}
			prevActivity = act
			i += n
			continue
		}
		sp.height = b.Height(m.width - ind)
		spans[i] = sp
		row += sp.height
		prevActivity = act
		i++
	}
	return spans
}

// totalLayoutRows is the height of the whole conversation: every block
// span plus its separator, plus the blank row that separates the
// streaming tail from an activity block (the tail is prose voice).
func (m Model) totalLayoutRows(spans []span) int {
	total := 0
	for _, s := range spans {
		if s.sepBefore {
			total++
		}
		total += s.height
	}
	if len(spans) > 0 && m.blocks[len(m.blocks)-1].Activity() {
		total++ // blank row above the streaming tail
	}
	return total
}

// leaderRunLen reports the run of collapsed read-only tool-end blocks
// starting at i, itself included. Runs need at least two members; a
// block that is live, failed, expanded, or not read-only never leads.
func (m Model) leaderRunLen(i int) int {
	head := m.blocks[i]
	class := render.ReadOnlyToolClass(head.Header.Label)
	if class == "" || head.Kind != uievent.KindToolEnd || !head.Collapsible || !head.Collapsed || head.Header.Role == theme.RoleDanger {
		return 0
	}
	n := 1
	for j := i + 1; j < len(m.blocks); j++ {
		b := m.blocks[j]
		if b.Kind != uievent.KindToolEnd || !b.Collapsible || !b.Collapsed ||
			render.ReadOnlyToolClass(b.Header.Label) != class ||
			b.Header.Role == theme.RoleDanger {
			break
		}
		n++
	}
	if n < 2 {
		return 0
	}
	return n
}

// expandRun opens every member of the leader run that starts at i, the
// way clicking the leader row or pressing space on it means "show me
// these": the run dissolves back into per-block headers, each still
// individually collapsible.
func (m *Model) expandRun(i int) {
	n := m.leaderRunLen(i)
	if n == 0 {
		return
	}
	m.blocks = slicesCloneBlocks(m.blocks)
	for k := i; k < i+n; k++ {
		m.blocks[k].Collapsed = false
	}
	m.clampOffset()
}

// leaderHeadOf reports the head index of the leader run that block i
// belongs to, when one is. A hidden run member has no visible row of
// its own, so its interactions route to the run's head.
func (m Model) leaderHeadOf(i int) (int, bool) {
	if m.leaderRunLen(i) > 0 {
		return i, true
	}
	for j := i - 1; j >= 0; j-- {
		if n := m.leaderRunLen(j); n > 0 && j+n > i {
			return j, true
		}
	}
	return 0, false
}

// leaderRow renders one collapsed read-only run as its single row:
// "> Read 3 files: a.go, b.go, c.go  3 files". Focus on any member
// draws the row reverse-video, the same treatment a lone block's header
// gets (ux-rules 6.3-6.4: shape carries the state, colour only helps).
func (m Model) leaderRow(s span, i int) string {
	head := m.blocks[i]
	n := s.runSize
	label, noun := "Read", "files"
	if render.ReadOnlyToolClass(head.Header.Label) == "search" {
		label, noun = "Searched", "queries"
	}
	targets := make([]string, 0, n)
	for k := i; k < i+n; k++ {
		if target := leaderTarget(m.blocks[k].Header.Detail); target != "" {
			targets = append(targets, target)
		}
	}
	spec := render.HeaderSpec{
		Marker: ">",
		Label:  label,
		Detail: strings.Join(targets, ", "),
		Meta:   itoa(n) + " " + noun,
	}
	headerW := m.width - s.indent
	if headerW > uikitconfig.ProseMeasureWide+16 {
		headerW = uikitconfig.ProseMeasureWide + 16
	}
	focused := false
	for k := i; k < i+n; k++ {
		focused = focused || m.blocks[k].Focused
	}
	if focused {
		return render.Role(m.Theme, m.Tier, theme.RoleFG).Reverse(true).
			Render(ansi.Strip(render.Header(m.Theme, m.Tier, headerW, spec)))
	}
	return render.Header(m.Theme, m.Tier, headerW, spec)
}

// leaderTarget shortens one member's header detail for the run's target
// list: a file path collapses to its base name, a search pattern stays
// whole (it is already short by contract). An empty detail contributes
// nothing - the count column still states the run's size.
func leaderTarget(detail string) string {
	if detail == "" {
		return ""
	}
	if base := filepath.Base(detail); base != "/" && base != "." && base != string(filepath.Separator) {
		return base
	}
	return detail
}

// renderSpanRows draws one block's rows at its span geometry: the group
// indent prefixes every row, and the block renders at the correspondingly
// narrower width so its internal wrap math still fits the terminal.
func (m Model) renderSpanRows(b Block, s span) []string {
	lines := strings.Split(b.Render(m.Theme, m.Tier, m.width-s.indent), "\n")
	if s.indent == 0 {
		return lines
	}
	pad := strings.Repeat(" ", s.indent)
	out := make([]string, len(lines))
	for j, line := range lines {
		out[j] = pad + line
	}
	return out
}

// itoa is the transcript's tiny integer formatter for row rendering.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// slicesCloneBlocks shallow-copies the block slice (Bodies are shared
// and treated as immutable by every mutator here).
func slicesCloneBlocks(in []Block) []Block {
	out := make([]Block, len(in))
	copy(out, in)
	return out
}
