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
	// top is the block's FIRST CONTENT row - the header row, or the
	// leader row when the block heads a coalesced run. A separator row
	// sits ABOVE it, outside the span, so top is exactly the row a click
	// on the header reports and exactly the row ScrollToFocus must
	// bring into view.
	top       int
	height    int  // terminal rows the block owns (0 when hidden in a run)
	indent    int  // columns the block is shifted by (groupIndent when activity)
	sepBefore bool // a blank separator row sits directly above top
	// runSize is 0 for a normal block, or the number of collapsed
	// read-only children this block's leader row stands in for
	// (itself included). A run member that is not the head draws
	// nothing; its top points at the leader's row so focus and click
	// routing land on the row the user can see.
	runSize int
	runTop  int     // row of the run's leader row (== top for the head)
	runKind runKind // which summary row draws the run
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
		sp := span{indent: ind, sepBefore: i > 0 && !(prevActivity && act)}
		if sp.sepBefore {
			row++ // the separator belongs above the span, not inside it
		}
		sp.top = row
		if n, kind := m.runAt(i); n > 0 {
			sp.height, sp.runSize, sp.runTop, sp.runKind = 1, n, row, kind
			spans[i] = sp
			row++
			for k := 1; k < n; k++ {
				spans[i+k] = span{top: row - 1, height: 0, indent: ind, runSize: n, runTop: row - 1, runKind: kind}
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

// runKind names the two ways consecutive blocks coalesce into one row.
type runKind uint8

const (
	runNone runKind = iota
	// runReadOnly is R2's run: two or more same-class read-only lookups,
	// drawn as "Read 3 files: a.go, b.go, c.go".
	runReadOnly
	// runWork is a wall of finished mixed activity - the reasoning, tool
	// calls and tool results between two assistant messages - drawn as
	// one row stating what ran, how many calls it made, and how long
	// they took.
	runWork
)

// minWorkRun is the number of finished activity blocks that make a wall.
// Two headers are not a wall, and folding them would cost the reader two
// tool names to save one row.
const minWorkRun = 3

// runAt reports the coalesced run starting at block i: how many blocks
// it holds, and which summary row draws it.
//
// The two kinds are measured independently and the LONGER one wins, so a
// wall that happens to open with a pair of reads still folds as one work
// run instead of fragmenting into a read pair plus loose headers. A tie
// goes to the read-only row, which names its targets and so says
// strictly more about the same blocks.
//
// The decision is two-way, not three: every block readOnlyRunLen counts
// also passes settledWork, contiguously from i, so workRunLen(i) is
// always at least readOnlyRunLen(i). A "work wins while being no longer
// than the read run" arm would be unreachable.
func (m Model) runAt(i int) (int, runKind) {
	ro := m.readOnlyRunLen(i)
	if work := m.workRunLen(i); work > ro && work >= minWorkRun {
		return work, runWork
	}
	if ro >= 2 {
		return ro, runReadOnly
	}
	return 0, runNone
}

// leaderRunLen is runAt's length alone, for the callers that only need
// to know whether block i heads a run.
func (m Model) leaderRunLen(i int) int {
	n, _ := m.runAt(i)
	return n
}

// readOnlyRunLen reports the run of collapsed same-class read-only
// tool-end blocks starting at i, itself included. A block that is live,
// failed, expanded, or not read-only never leads one.
func (m Model) readOnlyRunLen(i int) int {
	head := m.blocks[i]
	class := render.ReadOnlyToolClass(head.Header.Label)
	if class == "" || !m.settledWork(head) {
		return 0
	}
	n := 1
	for j := i + 1; j < len(m.blocks); j++ {
		b := m.blocks[j]
		if !m.settledWork(b) || b.Kind != uievent.KindToolEnd ||
			render.ReadOnlyToolClass(b.Header.Label) != class {
			break
		}
		n++
	}
	if n < 2 {
		return 0
	}
	return n
}

// workRunLen reports the run of finished, folded activity blocks
// starting at i: tool calls that have ended and reasoning that has
// flushed, each already collapsed.
func (m Model) workRunLen(i int) int {
	n := 0
	for j := i; j < len(m.blocks); j++ {
		if !m.settledWork(m.blocks[j]) {
			break
		}
		n++
	}
	return n
}

// settledWork reports a block that may disappear into a summary row.
//
// FINISHED is the load-bearing word. A live block is the one thing the
// reader is waiting on, so it never joins a run: work still running
// stays open and visible, and only what has already happened folds away.
//
// A failure never coalesces either. A run that could swallow a failed
// call would make the one block the reader must see the one block the
// screen hides - which is why RoleDanger is checked here rather than
// left to the per-kind rules.
func (m Model) settledWork(b Block) bool {
	if !b.Collapsible || !b.Collapsed || b.Header.Role == theme.RoleDanger {
		return false
	}
	return b.Kind == uievent.KindToolEnd || b.Kind == uievent.KindReasoning
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

// leaderHeadOf reports the head index of the run block i belongs to. A
// hidden run member has no visible row of its own, so its interactions
// route to the run's head.
//
// The runs are walked from the START, exactly the way layout() consumes
// them, because ANY SUFFIX of a run is itself a run: asking runAt(i)
// directly answered "yes, i heads a run" for a member halfway down one,
// and the keyboard toggle then expanded only from there. The row the
// user acted on was replaced by a different summary row with the earlier
// members still folded - not the dissolve into per-block headers that
// expandRun and ToggleFocused both promise. The mouse path was never
// wrong because it takes the head from the span.
func (m Model) leaderHeadOf(i int) (int, bool) {
	if i < 0 || i >= len(m.blocks) {
		return 0, false
	}
	for j := 0; j < len(m.blocks); {
		n, _ := m.runAt(j)
		if n == 0 {
			j++
			continue
		}
		if i >= j && i < j+n {
			return j, true
		}
		j += n
	}
	return 0, false
}

// leaderRow renders one coalesced run as its single row: the R2 read row
// ("> Read 3 files: a.go, b.go, c.go  3 files") or the work row
// ("> work  read_file, edit  5 calls  8.4s"). Focus on any member draws
// the row reverse-video, the same treatment a lone block's header gets
// (ux-rules 6.3-6.4: shape carries the state, colour only helps).
func (m Model) leaderRow(s span, i int) string {
	spec := m.readOnlyRunSpec(s, i)
	if s.runKind == runWork {
		spec = m.workRunSpec(s, i)
	}
	headerW := m.width - s.indent
	if headerW > uikitconfig.ProseMeasureWide+16 {
		headerW = uikitconfig.ProseMeasureWide + 16
	}
	focused := false
	for k := i; k < i+s.runSize; k++ {
		focused = focused || m.blocks[k].Focused
	}
	if focused {
		return render.Role(m.Theme, m.Tier, theme.RoleFG).Reverse(true).
			Render(ansi.Strip(render.Header(m.Theme, m.Tier, headerW, spec)))
	}
	return render.Header(m.Theme, m.Tier, headerW, spec)
}

// readOnlyRunSpec is the R2 row: "Read 3 files: a.go, b.go, c.go".
func (m Model) readOnlyRunSpec(s span, i int) render.HeaderSpec {
	n := s.runSize
	label, noun := "Read", "files"
	if render.ReadOnlyToolClass(m.blocks[i].Header.Label) == "search" {
		label, noun = "Searched", "queries"
	}
	targets := make([]string, 0, n)
	for k := i; k < i+n; k++ {
		if target := leaderTarget(m.blocks[k].Header.Detail); target != "" {
			targets = append(targets, target)
		}
	}
	return render.HeaderSpec{
		Marker: ">",
		Label:  label,
		Detail: strings.Join(targets, ", "),
		Meta:   itoa(n) + " " + noun,
	}
}

// workRunSpec is the wall's row: "work  read_file, edit, run_command
// 5 calls  8.4s". It answers the three questions a reader has about
// activity they are being shown one line of - what ran, how much of it
// there was, and what it cost - so the fold is a summary rather than a
// hiding place.
//
// The duration is the SUM of the calls' own durations, which is what the
// blocks carry; calls the loop issued in parallel therefore add up to
// more than the wall clock. Sum is still the honest answer to "what did
// this work cost", and it is the only one the model holds: no block
// records when the run started.
func (m Model) workRunSpec(s span, i int) render.HeaderSpec {
	n := s.runSize
	var (
		names   []string
		seen    = make(map[string]bool, n)
		calls   int
		elapsed int
	)
	for k := i; k < i+n; k++ {
		b := m.blocks[k]
		if b.Kind == uievent.KindToolEnd {
			calls++
		}
		elapsed += b.ElapsedMS
		if label := b.Header.Label; label != "" && !seen[label] {
			seen[label] = true
			names = append(names, label)
		}
	}
	detail := strings.Join(names, ", ")
	if len(names) > workRunNameLimit {
		detail = strings.Join(names[:workRunNameLimit], ", ") +
			" +" + itoa(len(names)-workRunNameLimit) + " more"
	}
	// A run of nothing but reasoning made no calls, so it is counted in
	// steps. Saying "0 calls" would be true and useless.
	meta := itoa(calls) + " calls"
	if calls == 0 {
		meta = itoa(n) + " steps"
	}
	if elapsed > 0 {
		meta += "  " + render.FormatElapsed(elapsed)
	}
	return render.HeaderSpec{Marker: ">", Label: "work", Detail: detail, Meta: meta}
}

// workRunNameLimit is how many distinct tool names the work row lists
// before it counts the rest. Past three the list stops naming the work
// and starts clipping, and the count survives the clip where a name
// would not.
const workRunNameLimit = 3

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
