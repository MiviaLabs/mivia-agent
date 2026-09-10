package transcript

// The streaming tail: text and reasoning deltas accumulate in Model's
// pending buffer instead of committing a block per token, a repaint
// clock (FlushMsg) redraws the tail while a span is live, and
// flushPending commits the span as a finished block when the next
// top-level event arrives. Everything here reads and writes the pending
// fields declared on Model in transcript.go; the block-handling code
// there calls flushPending before it pushes any new block.

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

// flushPending commits any in-flight text or reasoning span as a
// finished prose block. Every event that starts a new top-level block
// must call this first unless it already carries its own final text
// (text.end, and the terminal reasoning.delta both replace pending
// rather than continue it - see their own case bodies). Skipping the
// flush silently drops the partial span, and the next block's own first
// row can then visually collide with the abandoned streaming tail.
//
// flushPending renders through render.Markdown (not the raw partial
// bytes) for streaming-parity with text.end: the live streaming tail
// is plain text styled via render.Wrap, the committed text.end block
// is markdown, and a partial stream that gets bumped by a tool.start /
// turn.start / plan / error / unfinished-turn.end event used to land as
// raw text. The two rendering paths disagreed on heading chrome, list
// markers, and code fences - a partial stream that contained "# "
// would render as '# heading' in the streaming tail and as styled bold
// in the committed block. Routing flushPending through the same
// renderer as text.end makes a bump-mid-stream indistinguishable from
// a clean text.end, which is the contract the user picked.
func (m Model) flushPending() Model {
	partial := m.pending
	if partial == "" {
		return m
	}
	kind := m.pendingKind
	started := m.pendingStartedAt
	m.clearPending()
	if kind == uievent.KindReasoning {
		words := len(strings.Fields(partial))
		m, _ = m.pushBlock(m.reasoningBlockFrom(partial, words, started))
		return m
	}
	m, _ = m.pushBlock(Block{
		Kind:  uievent.KindTextEnd,
		Prose: true,
		Body:  proseLines(render.Markdown(m.Theme, m.Tier, m.proseRenderWidth(), partial)),
	})
	return m
}

func (m Model) handleReasoningDelta(b uievent.ReasoningDeltaBody) (Model, tea.Cmd) {
	// Providers may return reasoning only after they streamed answer text.
	// That text is still pending and text.end will commit it. Do not switch
	// the shared pending span to reasoning: appendPending would flush the
	// answer early, then text.end would commit the same answer again.
	if m.pendingKind == uievent.KindTextDelta {
		if b.Text == "" {
			return m, nil
		}
		words := b.WordCount
		if words == 0 {
			words = len(strings.Fields(b.Text))
		}
		// Whole-text path (C1): no reasoning was pending, so there is no
		// start time to inherit - this instant IS the start.
		return m.pushBlock(m.reasoningBlockFrom(b.Text, words, m.now()))
	}
	if b.WordCount == 0 {
		return m, m.appendPending(uievent.KindReasoning, b.Text)
	}
	raw := m.pending
	started := m.pendingStartedAt
	if raw == "" && b.Text != "" {
		// Whole-text path (C1): a single atomic reasoning event with no
		// prior delta buffered - same "this instant is the start" rule.
		raw = b.Text
		started = m.now()
	}
	m.clearPending()
	return m.pushBlock(m.reasoningBlockFrom(raw, b.WordCount, started))
}

// reasoningBlock builds a settled reasoning Block from its accumulated
// text and word count, with no timing: reasoningBlockFrom is what stamps
// StartedAt/ElapsedMS, because that needs the model's clock and this
// does not.
func reasoningBlock(text string, words int) Block {
	var body []string
	if text != "" {
		body = strings.Split(strings.TrimRight(text, "\n"), "\n")
	}
	return Block{
		Kind:        uievent.KindReasoning,
		Collapsible: true,
		Collapsed:   true,
		Header: Header{
			Label: "reasoning",
			Meta:  fmt.Sprintf("%d words", words),
			State: "hidden",
		},
		Body: body,
	}
}

// reasoningBlockFrom builds a settled reasoning Block and stamps its
// StartedAt/ElapsedMS from started (C1), using this model's clock for
// "now" the same way handleToolEnd's measured-duration fallback does.
// started is the zero time only when the caller has genuinely observed
// no start (never true on any live path); ElapsedMS stays 0 then rather
// than measuring against the zero time, which would report a duration of
// decades.
func (m Model) reasoningBlockFrom(text string, words int, started time.Time) Block {
	blk := reasoningBlock(text, words)
	if started.IsZero() {
		return blk
	}
	blk.StartedAt = started
	blk.ElapsedMS = int(m.now().Sub(started) / time.Millisecond)
	return blk
}

func (m *Model) clearPending() {
	m.pending = ""
	m.pendingKind = ""
	m.pendingStartedAt = time.Time{}
}

func (m *Model) appendPending(kind uievent.Kind, text string) tea.Cmd {
	// A change of kind ends the previous span. Without this, a text delta
	// arriving after reasoning deltas concatenates into the same buffer and
	// the reasoning renders as prose, attributed to the model's answer.
	if m.pendingKind != "" && m.pendingKind != kind && m.pending != "" {
		flushed := m.flushPending()
		*m = flushed
	}
	// Stamp the start of a fresh reasoning span (C1), not a continuation
	// of one already streaming: pendingKind is "" the first time a
	// reasoning delta arrives (idle, or just flushed above), and stays
	// KindReasoning on every subsequent chunk of the SAME span.
	if kind == uievent.KindReasoning && m.pendingKind != uievent.KindReasoning {
		m.pendingStartedAt = m.now()
	}
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
//
// A streaming REASONING span (C1) does not preview its raw text: like
// the settled block, the reader wants to know how long the model has
// been thinking, not read a half-formed chain of thought. The row reads
// "Thinking  Xs", refreshed by the same flush clock that already redraws
// this tail (transcript.go's doc comment; Update, above) - no new clock
// is armed for it (.agents/memories/tui-spinner-clock-*.md).
func (m Model) tailRows() []string {
	if m.pending == "" {
		return nil
	}
	if m.pendingKind == uievent.KindReasoning {
		style := render.Role(m.Theme, m.Tier, theme.RoleFGSubtle).Italic(true)
		elapsed := 0
		if !m.pendingStartedAt.IsZero() {
			elapsed = int(m.now().Sub(m.pendingStartedAt) / time.Millisecond)
		}
		return []string{style.Render("Thinking  " + render.FormatElapsed(elapsed))}
	}
	style := render.Role(m.Theme, m.Tier, theme.RoleFG)
	measure := render.ProseMeasure(m.width)
	var out []string
	for _, line := range strings.Split(m.pending, "\n") {
		for _, row := range render.Wrap(line, measure) {
			out = append(out, style.Render(row))
		}
	}
	return out
}
