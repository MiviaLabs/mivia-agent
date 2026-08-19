// Package transcript renders the conversation history for the inline-first
// UI: a bounded window of committed blocks plus, while a message is
// streaming, a live uncommitted tail. It handles every uievent.Kind
// exhaustively, mirroring internal/ui/stream's plain-text renderer but
// styled through internal/ui/theme.
package transcript

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// Model renders a bounded window of the conversation as styled text. Text
// and reasoning deltas accumulate in a live buffer instead of committing
// a block per token (build spec section 4.5: "one Msg per token is one
// render per token even with the cell renderer"); HandleEvent returns a
// tea.Cmd that starts a repaint clock while a span is streaming.
type Model struct {
	Theme theme.Theme
	Tier  theme.Tier

	blocks    []string
	maxBlocks int

	pending     strings.Builder
	pendingKind uievent.Kind // uievent.KindTextDelta or KindReasoning while streaming; "" when idle
	flushWait   bool
}

// New returns an empty Model bounded to uikit/config.MaxTranscriptLines
// committed blocks.
func New(t theme.Theme, tier theme.Tier) Model {
	return Model{Theme: t, Tier: tier, maxBlocks: uikitconfig.MaxTranscriptLines}
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
	case uievent.TurnStartBody:
		m.commit(render.Role(m.Theme, m.Tier, theme.RoleAccent).Render("> ") + b.Input)
	case uievent.TextDeltaBody:
		cmd := m.appendPending(uievent.KindTextDelta, b.Text)
		return m, cmd
	case uievent.TextEndBody:
		m.pending.Reset()
		m.pendingKind = ""
		if b.Text != "" {
			m.commit(render.Text(m.Theme, m.Tier, b.Text))
		}
	case uievent.ReasoningDeltaBody:
		if b.WordCount > 0 {
			m.pending.Reset()
			m.pendingKind = ""
			m.commit(render.Role(m.Theme, m.Tier, theme.RoleFGSubtle).Render(
				fmt.Sprintf("reasoning: %d words hidden", b.WordCount)))
			return m, nil
		}
		cmd := m.appendPending(uievent.KindReasoning, b.Text)
		return m, cmd
	case uievent.ToolPendingBody:
		m.commit(render.Role(m.Theme, m.Tier, theme.RoleWarning).Render(
			fmt.Sprintf("? approve %s %s", b.Name, render.FormatArgs(b.Args))))
	case uievent.ToolStartBody:
		m.commit(render.Role(m.Theme, m.Tier, theme.RoleInfo).Render(
			fmt.Sprintf("v %s %s", b.Name, render.FormatArgs(b.Args))))
	case uievent.ToolOutputBody:
		m.commit(toolOutputLine(m.Theme, m.Tier, b))
	case uievent.ToolEndBody:
		m.commit(toolEndBlock(m.Theme, m.Tier, b))
	case uievent.PlanBody:
		m.commit(planBlock(m.Theme, m.Tier, b))
	case uievent.NoticeBody:
		m.commit(render.Role(m.Theme, m.Tier, theme.RoleInfo).Render(b.Text))
	case uievent.ErrorBody:
		m.commit(render.Role(m.Theme, m.Tier, theme.RoleDanger).Render(b.Text))
	case uievent.UsageBody:
		m.commit(render.Role(m.Theme, m.Tier, theme.RoleFGMuted).Render(
			fmt.Sprintf("%d in / %d out / %d cached / $%.3f", b.InputTokens, b.OutputTokens, b.CachedTokens, b.CostUSD)))
	case uievent.TurnEndBody:
		// No block: turn-state belongs to the statusline component, not
		// the transcript.
	}
	return m, nil
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

func (m *Model) commit(block string) {
	if block == "" {
		return
	}
	m.blocks = append(m.blocks, block)
	if len(m.blocks) > m.maxBlocks {
		m.blocks = m.blocks[len(m.blocks)-m.maxBlocks:]
	}
}

// View renders every committed block plus, if a span is streaming, its
// live uncommitted tail.
func (m Model) View() string {
	lines := make([]string, 0, len(m.blocks)+1)
	lines = append(lines, m.blocks...)
	if m.pending.Len() > 0 {
		style := render.Role(m.Theme, m.Tier, theme.RoleFG)
		if m.pendingKind == uievent.KindReasoning {
			style = render.Role(m.Theme, m.Tier, theme.RoleFGSubtle)
		}
		lines = append(lines, style.Render(m.pending.String()))
	}
	return strings.Join(lines, "\n")
}

func toolOutputLine(t theme.Theme, tier theme.Tier, b uievent.ToolOutputBody) string {
	style := render.Role(t, tier, theme.RoleFGMuted)
	if b.Progress != nil {
		p := b.Progress
		return style.Render(fmt.Sprintf("  [%d/%d] %s %.0fs", p.Step, p.TotalSteps, p.Status, p.ElapsedSeconds))
	}
	if b.Chunk == "" {
		return ""
	}
	first := strings.SplitN(b.Chunk, "\n", 2)[0]
	return style.Render("  " + first)
}

func toolEndBlock(t theme.Theme, tier theme.Tier, b uievent.ToolEndBody) string {
	role, status := theme.RoleSuccess, "ok"
	if !b.OK {
		role, status = theme.RoleDanger, "failed"
	}
	summary := b.Result
	if b.Err != "" {
		summary = b.Err
	}
	line := render.Role(t, tier, role).Render(fmt.Sprintf("%-12s %-6s %6dms  %s", b.Name, status, b.DurationMS, summary))
	if b.Diff == nil {
		return line
	}
	return line + "\n" + render.Diff(t, tier, *b.Diff)
}

func planBlock(t theme.Theme, tier theme.Tier, b uievent.PlanBody) string {
	lines := make([]string, 0, len(b.Items)+1)
	lines = append(lines, render.Role(t, tier, theme.RoleFGMuted).Render(fmt.Sprintf("plan %d/%d", b.Done, b.Total)))
	for _, item := range b.Items {
		mark, style := "[ ]", render.Role(t, tier, theme.RoleFG)
		if item.Done {
			mark, style = "[x]", render.Role(t, tier, theme.RoleFGSubtle)
		}
		lines = append(lines, style.Render(fmt.Sprintf("  %s %s", mark, item.Text)))
	}
	return strings.Join(lines, "\n")
}
