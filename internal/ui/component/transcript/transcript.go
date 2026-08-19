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

// Model holds the live window, the retained ring, and the in-flight
// streaming tail. Text and reasoning deltas accumulate in a buffer
// instead of committing a block per token (build spec section 4.5: "one
// Msg per token is one render per token even with the cell renderer");
// HandleEvent returns a tea.Cmd that starts a repaint clock while a span
// is streaming.
type Model struct {
	Theme theme.Theme
	Tier  theme.Tier

	blocks []Block // live window, oldest first
	ring   []Block // retained after eviction, oldest first
	focus  int     // index into blocks; -1 when the composer has focus

	width, height, reserved int

	nextID      int
	pending     strings.Builder
	pendingKind uievent.Kind // uievent.KindTextDelta or KindReasoning while streaming; "" when idle
	flushWait   bool
}

// New returns an empty Model with no block focused.
func New(t theme.Theme, tier theme.Tier) Model {
	return Model{Theme: t, Tier: tier, focus: -1}
}

// CommitMsg carries evicted content, already ordered and joined, for the
// caller to print above the managed frame. transcript.Model does not call
// tea.Println itself: Println's Msg type is unexported, which would leave
// this package's own tests unable to assert on committed content.
//
// One message carries every block evicted by a single event, joined by
// newlines, because tea.Batch documents "no ordering guarantees" - one
// print Cmd per block would scramble scrollback.
type CommitMsg struct{ Text string }

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
	if m.pending.Len() > 0 {
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
		return m, m.appendPending(uievent.KindTextDelta, b.Text)
	case uievent.ReasoningDeltaBody:
		if b.WordCount == 0 {
			return m, m.appendPending(uievent.KindReasoning, b.Text)
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
			Body:  strings.Split(render.Text(m.Theme, m.Tier, b.Text), "\n"),
		})
	case uievent.TurnStartBody:
		return m.pushBlock(Block{
			Kind:  uievent.KindTurnStart,
			Prose: true,
			Body:  userLines(m.Theme, m.Tier, b.Input),
		})
	case uievent.ToolPendingBody:
		return m.pushBlock(Block{
			Kind: uievent.KindToolPending,
			Header: Header{
				Label: b.Name, Detail: render.FormatArgs(b.Args),
				State: "pending", Role: theme.RoleWarning,
			},
		})
	case uievent.ToolStartBody:
		return m.pushBlock(Block{
			Kind: uievent.KindToolStart,
			Header: Header{
				Label: b.Name, Detail: render.FormatArgs(b.Args),
				State: "running", Role: theme.RoleInfo,
			},
		})
	case uievent.ToolOutputBody:
		return m.pushBlock(toolOutputBlock(m.Theme, m.Tier, b))
	case uievent.ToolEndBody:
		return m.pushBlock(toolEndBlockValue(m.Theme, m.Tier, b))
	case uievent.PlanBody:
		return m.pushBlock(planBlockValue(m.Theme, m.Tier, b))
	case uievent.NoticeBody:
		return m.pushBlock(Block{
			Kind:   uievent.KindNotice,
			Header: Header{Label: "notice", Detail: b.Text, Role: theme.RoleInfo},
		})
	case uievent.ErrorBody:
		return m.pushBlock(errorBlockValue(b))
	case uievent.UsageBody:
		return m.pushBlock(Block{
			Kind: uievent.KindUsage,
			Header: Header{
				Label: "usage",
				Detail: fmt.Sprintf("%d in  %d out  %d cached  $%.3f",
					b.InputTokens, b.OutputTokens, b.CachedTokens, b.CostUSD),
			},
		})
	case uievent.TurnEndBody:
		// No block: turn-state belongs to the statusline component, not
		// the transcript.
	}
	return m, nil
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
	return m, commit(m.push(b))
}

func (m *Model) clearPending() {
	m.pending.Reset()
	m.pendingKind = ""
}

// userLines renders the user's turn: the accent marker on the first row,
// continuation rows aligned under the text (wireframes-panes.md 4).
func userLines(t theme.Theme, tier theme.Tier, input string) []string {
	marker := render.Role(t, tier, theme.RoleAccent).Render("> ")
	raw := strings.Split(input, "\n")
	out := make([]string, 0, len(raw))
	for i, line := range raw {
		if i == 0 {
			out = append(out, marker+line)
			continue
		}
		out = append(out, "  "+line)
	}
	return out
}

func (m *Model) appendPending(kind uievent.Kind, text string) tea.Cmd {
	m.pending.WriteString(text)
	m.pendingKind = kind
	if m.flushWait {
		return nil
	}
	m.flushWait = true
	return flushCmd()
}

// commit returns the Cmd that delivers block as a CommitMsg, or nil for
// an empty block (a Kind that produced no visible content).
func commit(block string) tea.Cmd {
	if block == "" {
		return nil
	}
	return func() tea.Msg { return CommitMsg{Text: block} }
}

// View renders the live window and then the still-streaming tail. It is
// bounded by the eviction budget by construction, which is what keeps it
// compatible with Bubble Tea's inline redraw.
func (m Model) View() string {
	rows := make([]string, 0, len(m.blocks)+1)
	for _, b := range m.blocks {
		rows = append(rows, b.Render(m.Theme, m.Tier, m.width))
	}
	if m.pending.Len() > 0 {
		style := render.Role(m.Theme, m.Tier, theme.RoleFG)
		if m.pendingKind == uievent.KindReasoning {
			style = render.Role(m.Theme, m.Tier, theme.RoleFGSubtle)
		}
		rows = append(rows, style.Render(m.pending.String()))
	}
	return strings.Join(rows, "\n")
}

func toolOutputBlock(t theme.Theme, tier theme.Tier, b uievent.ToolOutputBody) Block {
	if b.Progress != nil {
		p := b.Progress
		return Block{
			Kind: uievent.KindToolOutput,
			Header: Header{
				Label:  "subagent",
				Meta:   fmt.Sprintf("%d of %d", p.Step, p.TotalSteps),
				State:  p.Status,
				Role:   theme.RoleInfo,
				Detail: fmt.Sprintf("%.0fs", p.ElapsedSeconds),
			},
			Body: p.Log,
		}
	}
	if b.Chunk == "" {
		return Block{}
	}
	// Keep every line of tool output. Discarding all but the first was a
	// silent truncation with nothing to signal it.
	return Block{
		Kind:   uievent.KindToolOutput,
		Header: Header{Label: "output"},
		Body:   strings.Split(strings.TrimRight(b.Chunk, "\n"), "\n"),
	}
}

func toolEndBlockValue(t theme.Theme, tier theme.Tier, b uievent.ToolEndBody) Block {
	role, status := theme.RoleSuccess, "ok"
	if !b.OK {
		role, status = theme.RoleDanger, "failed"
	}
	summary := b.Result
	if b.Err != "" {
		summary = b.Err
	}
	blk := Block{
		Kind: uievent.KindToolEnd,
		Header: Header{
			Label: b.Name, Detail: summary,
			Meta: fmt.Sprintf("%dms", b.DurationMS), State: status, Role: role,
		},
	}
	if b.Diff != nil {
		blk.Header.Detail = b.Diff.Path
		blk.Header.Meta = fmt.Sprintf("+%d -%d  %dms", b.Diff.Added, b.Diff.Removed, b.DurationMS)
		blk.Body = strings.Split(render.Diff(t, tier, *b.Diff), "\n")
	}
	return blk
}

func planBlockValue(t theme.Theme, tier theme.Tier, b uievent.PlanBody) Block {
	body := make([]string, 0, len(b.Items))
	for _, item := range b.Items {
		mark, style := "[ ]", render.Role(t, tier, theme.RoleFG)
		if item.Done {
			mark, style = "[x]", render.Role(t, tier, theme.RoleFGSubtle)
		}
		body = append(body, style.Render(mark+" "+item.Text))
	}
	return Block{
		Kind:   uievent.KindPlan,
		Header: Header{Label: "plan", Meta: fmt.Sprintf("%d of %d", b.Done, b.Total)},
		Body:   body,
	}
}

func errorBlockValue(b uievent.ErrorBody) Block {
	lines := strings.Split(b.Text, "\n")
	state := ""
	if b.Fatal {
		state = "fatal"
	}
	return Block{
		Kind:   uievent.KindError,
		Header: Header{Label: "error", Detail: lines[0], State: state, Role: theme.RoleDanger},
		Body:   lines[1:],
	}
}
