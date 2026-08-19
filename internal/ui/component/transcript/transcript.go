// Package transcript renders the conversation for the inline-first UI.
// It handles every uievent.Kind exhaustively, mirroring internal/ui/
// stream's plain-text renderer but styled through internal/ui/theme.
//
// Three layers, in decreasing order of power:
//
//   - The live window holds the newest blocks whose total height fits the
//     viewport budget. They are values, not strings, so they re-render:
//     they take focus, collapse, and update state in place.
//   - The retained ring holds what left the live window, bounded by
//     config.MaxTranscriptLines, so a pager can still read it.
//   - Terminal scrollback holds every evicted block, printed once by the
//     caller. Frozen text, but natively selectable and searchable.
//
// The trigger matters. A block commits when it is EVICTED, not when it
// is finalized. Conflating the two is what makes a transcript
// non-interactive: a finalized block is often still on screen, and while
// it is on screen the user must be able to focus and collapse it.
//
// View() renders the live window plus the streaming tail, and is bounded
// by the budget by construction. That bound is the point: a View() taller
// than the terminal does not compose with Bubble Tea's inline redraw
// (relative cursor movement plus erase), and earlier content is erased
// before a user - or a test - can see it.
package transcript

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// Model holds the whole conversation, the viewport over it, and the
// in-flight streaming tail. Text and reasoning deltas accumulate in a
// buffer instead of committing a block per token (build spec section
// 4.5: "one Msg per token is one render per token even with the cell
// renderer"); HandleEvent returns a tea.Cmd that starts a repaint clock
// while a span is streaming.
type Model struct {
	Theme theme.Theme
	Tier  theme.Tier

	blocks  []Block // the conversation, oldest first
	dropped int     // blocks the bound discarded, stated in the view
	focus   int     // index into blocks; -1 when the composer has focus

	width, height int
	offset        int  // first visible row of the conversation
	follow        bool // new output pulls the view to the bottom
	missed        int  // finished blocks that arrived while paused (rule 6.7)

	nextID int
	// pending is a plain string, not a strings.Builder. Model is copied
	// on every HandleEvent, and a non-zero Builder panics when it is
	// written after a copy. The spans here are short-lived and tiny, so
	// the Builder bought nothing and risked a crash.
	pending     string
	pendingKind uievent.Kind // uievent.KindTextDelta or KindReasoning while streaming; "" when idle
	flushWait   bool

	// hideReasoning collapses every live reasoning block. It is a view
	// state, not a filter: the blocks stay in the window and in the ring.
	hideReasoning bool
}

// New returns an empty Model with no block focused, following the tail.
func New(t theme.Theme, tier theme.Tier) Model {
	return Model{Theme: t, Tier: tier, focus: -1, follow: true}
}

// FlushMsg ticks the repaint clock while a text/reasoning span streams.
type FlushMsg struct{}

func flushCmd() tea.Cmd {
	return tea.Tick(uikitconfig.TextDeltaFlushInterval, func(time.Time) tea.Msg { return FlushMsg{} })
}

// Update handles FlushMsg only; every other Msg is ignored, so this
// Model can sit inside a larger Update without a type-switch guard at
// the call site.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	if _, ok := msg.(FlushMsg); !ok {
		return m, nil
	}
	m.flushWait = false
	if m.pending != "" {
		// Still streaming (or awaiting the terminal chunk): keep the
		// repaint clock alive. One extra harmless tick lands right after
		// the span ends, since flushWait was already true when it did.
		m.flushWait = true
		return m, flushCmd()
	}
	return m, nil
}

// HandleEvent applies one uievent.Event to the model and returns the
// updated Model plus a Cmd exactly when a new streaming span needs its
// repaint clock started. Same value-receiver, return-new-Model shape as
// Update, so a caller has one calling convention for both instead of an
// in-place pointer mutation for one and a returned copy for the other.
func (m Model) HandleEvent(ev uievent.Event) (Model, tea.Cmd) {
	switch b := ev.Body.(type) {
	case uievent.TextDeltaBody:
		cmd := m.appendPending(uievent.KindTextDelta, b.Text)
		return m, cmd
	case uievent.ReasoningDeltaBody:
		if b.WordCount == 0 {
			cmd := m.appendPending(uievent.KindReasoning, b.Text)
			return m, cmd
		}
		m.clearPending()
		return m.pushBlock(Block{
			Kind:   uievent.KindReasoning,
			Header: Header{Label: "reasoning", Meta: fmt.Sprintf("%d words", b.WordCount), State: "hidden"},
		})
	case uievent.TextEndBody:
		m.clearPending()
		if b.Text == "" {
			return m, nil
		}
		return m.pushBlock(Block{
			Kind:  uievent.KindTextEnd,
			Prose: true,
			Body:  proseLines(render.Text(m.Theme, m.Tier, b.Text)),
		})
	case uievent.TurnStartBody:
		m = m.flushPending()
		return m.pushBlock(Block{
			Kind:  uievent.KindTurnStart,
			Prose: true,
			Input: b.Input,
			Body:  userLines(m.Theme, m.Tier, m.width, b.Input),
		})
	case uievent.ToolPendingBody, uievent.ToolStartBody, uievent.ToolOutputBody, uievent.ToolEndBody:
		m = m.flushPending()
		return m.handleToolEvent(ev.Body)
	case uievent.PlanBody:
		m = m.flushPending()
		return m.pushBlock(planBlockValue(m.Theme, m.Tier, b))
	case uievent.NoticeBody:
		m = m.flushPending()
		return m.pushBlock(Block{
			Kind:   uievent.KindNotice,
			Header: Header{Label: "notice", Detail: b.Text, Role: theme.RoleInfo},
		})
	case uievent.ErrorBody:
		m = m.flushPending()
		return m.pushBlock(errorBlockValue(b))
	case uievent.UsageBody:
		m = m.flushPending()
		return m.pushBlock(Block{
			Kind: uievent.KindUsage,
			Header: Header{
				Label: "usage",
				Detail: fmt.Sprintf("%d in  %d out  %d cached  $%.3f",
					b.InputTokens, b.OutputTokens, b.CachedTokens, b.CostUSD),
			},
		})
	case uievent.TurnEndBody:
		// A completed turn commits nothing: turn-state belongs to the
		// statusline. A turn that did NOT complete must say so, and must
		// keep whatever partial text had streamed. Dropping the partial
		// text with no explanation is the transcript lying about why it
		// stopped, which section 13 forbids.
		if b.Reason == "" || b.Reason == turnReasonCompleted {
			m.clearPending()
			return m, nil
		}
		return m.endTurnUnfinished(b.Reason)
	}
	return m, nil
}

// Clear empties the transcript: every block, the drop count, the
// focused block, and the in-flight streaming tail. Auto-follow resumes
// at the empty state, so new output appears immediately. The /clear
// command uses this.
func (m Model) Clear() Model {
	m.blocks = nil
	m.dropped = 0
	m.focus = -1
	m.offset = 0
	m.follow = true
	m.missed = 0
	m.pending = ""
	m.pendingKind = ""
	m.flushWait = false
	return m
}

// turnReasonCompleted is the one reason that commits no block.
const turnReasonCompleted = "completed"

// shortResultCols is the longest tool result that may sit in the header's
// meta column. Anything longer is a message, not a metric, and goes in
// the body where it can wrap instead of squeezing the detail out.
const shortResultCols = 16

// endTurnUnfinished flushes any partial stream as prose, then records
// why the turn stopped.
// flushPending commits any in-flight text or reasoning span as a
// finished prose block. Every event that starts a new top-level block
// must call this first unless it already carries its own final text
// (text.end, and the terminal reasoning.delta both replace pending
// rather than continue it - see their own case bodies). Skipping the
// flush silently drops the partial span, and the next block's own first
// row can then visually collide with the abandoned streaming tail.
func (m Model) flushPending() Model {
	partial := m.pending
	if partial == "" {
		return m
	}
	m.clearPending()
	m, _ = m.pushBlock(Block{
		Kind:  uievent.KindTextEnd,
		Prose: true,
		Body:  strings.Split(partial, "\n"),
	})
	return m
}

func (m Model) endTurnUnfinished(reason string) (Model, tea.Cmd) {
	m = m.flushPending()

	// Label only. TurnEndBody carries just the reason, so inventing a
	// detail or a duration here would be fabrication; section 13's
	// richer line lands when the contract carries those fields.
	return m.pushBlock(Block{
		Kind:   uievent.KindTurnEnd,
		Header: Header{Label: reason, Role: theme.RoleWarning},
	})
}

func (m Model) handleToolEvent(body uievent.Body) (Model, tea.Cmd) {
	switch b := body.(type) {
	case uievent.ToolPendingBody:
		return m.handleToolPending(b)
	case uievent.ToolStartBody:
		return m.handleToolStart(b)
	case uievent.ToolOutputBody:
		return m.handleToolOutput(b)
	case uievent.ToolEndBody:
		return m.handleToolEnd(b)
	}
	return m, nil
}

func (m Model) handleToolPending(b uievent.ToolPendingBody) (Model, tea.Cmd) {
	return m.pushBlock(Block{
		Kind: uievent.KindToolPending, CallID: b.ToolCallID,
		Header: Header{
			Label: b.Name, Detail: render.FormatArgs(b.Args),
			State: "pending", Role: theme.RoleWarning,
		},
	})
}

func (m Model) handleToolStart(b uievent.ToolStartBody) (Model, tea.Cmd) {
	if ok := m.updateLive(b.ToolCallID, func(blk *Block) {
		blk.Kind = uievent.KindToolStart
		blk.Header.State, blk.Header.Role = "running", theme.RoleInfo
		if d := render.FormatArgs(b.Args); d != "" {
			blk.Header.Detail = d
		}
	}); ok {
		return m, nil
	}
	return m.pushBlock(Block{
		Kind: uievent.KindToolStart, CallID: b.ToolCallID,
		Header: Header{
			Label: b.Name, Detail: render.FormatArgs(b.Args),
			State: "running", Role: theme.RoleInfo,
		},
	})
}

func (m Model) handleToolOutput(b uievent.ToolOutputBody) (Model, tea.Cmd) {
	lines := outputLines(b)
	if len(lines) == 0 {
		return m.pushBlock(toolOutputBlock(m.Theme, m.Tier, b))
	}
	if ok := m.updateLive(b.ToolCallID, func(blk *Block) {
		if p := b.Progress; p != nil {
			blk.Header.Meta = fmt.Sprintf("%d of %d", p.Step, p.TotalSteps)
			if p.Status != "" {
				blk.Header.State = p.Status
			}
			if p.ElapsedSeconds > 0 {
				blk.Header.Detail = fmt.Sprintf("%.0fs", p.ElapsedSeconds)
			}
			blk.Body = lines
			return
		}
		blk.Body = append(blk.Body, lines...)
	}); ok {
		return m, nil
	}
	return m.pushBlock(toolOutputBlock(m.Theme, m.Tier, b))
}

func (m Model) handleToolEnd(b uievent.ToolEndBody) (Model, tea.Cmd) {
	end := toolEndBlockValue(m.Theme, m.Tier, b)
	if ok := m.updateLive(b.ToolCallID, func(blk *Block) {
		blk.Kind = uievent.KindToolEnd
		detail := blk.Header.Detail
		result := end.Header.Detail
		blk.Header = end.Header
		if detail != "" && b.Diff == nil {
			blk.Header.Detail = detail
			switch {
			case result == "":
			case len(result) <= shortResultCols:
				blk.Header.Meta = result + "  " + blk.Header.Meta
			default:
				blk.Body = append([]string{result}, blk.Body...)
			}
		}
		if len(end.Body) > 0 {
			blk.Body = append(blk.Body, end.Body...)
		}
	}); ok {
		return m, nil
	}
	return m.pushBlock(end)
}

// pushBlock appends a block, evicts to fit, and returns the commit Cmd
// for whatever the push pushed out. A block with nothing to show is
// dropped rather than pushed: it would otherwise render as a bare
// collapse marker with no content beside it.
func (m Model) pushBlock(b Block) (Model, tea.Cmd) {
	if b.isEmpty() {
		return m, nil
	}
	m.nextID++
	b.ID = strconv.Itoa(m.nextID)
	m.push(b)
	return m, nil
}

func (m *Model) clearPending() {
	m.pending = ""
	m.pendingKind = ""
}

func (m *Model) appendPending(kind uievent.Kind, text string) tea.Cmd {
	m.pending += text
	m.pendingKind = kind
	if m.flushWait {
		return nil
	}
	m.flushWait = true
	return flushCmd()
}

// tailRows is the still-streaming span, drawn below the last finished
// block. It is separate from the blocks because it is not addressable:
// it has no header, cannot take focus, and is replaced wholesale when
// the span ends.
func (m Model) tailRows() []string {
	if m.pending == "" {
		return nil
	}
	style := render.Role(m.Theme, m.Tier, theme.RoleFG)
	if m.pendingKind == uievent.KindReasoning {
		style = render.Role(m.Theme, m.Tier, theme.RoleFGSubtle)
	}
	measure := render.ProseMeasure(m.width)
	var out []string
	for _, line := range strings.Split(m.pending, "\n") {
		for _, row := range render.Wrap(line, measure) {
			out = append(out, style.Render(row))
		}
	}
	return out
}

// SetTheme records a theme change, re-resolving theme colors and user turn blocks.
func (m *Model) SetTheme(t theme.Theme, tier theme.Tier) {
	m.Theme, m.Tier = t, tier
	for i := range m.blocks {
		if m.blocks[i].Kind == uievent.KindTurnStart {
			m.blocks[i].Body = userLines(m.Theme, m.Tier, m.width, m.blocks[i].Input)
		}
	}
}
