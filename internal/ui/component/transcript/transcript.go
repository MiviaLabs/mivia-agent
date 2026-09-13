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
	"slices"
	"strconv"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	sel "github.com/MiviaLabs/mivia-agent/internal/ui/select"
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

	// pendingStartedAt is when THIS transcript first saw the current
	// reasoning span begin (appendPending, on the switch into
	// KindReasoning), the same StartedAt/ElapsedMS timing contract
	// handleToolEnd already applies to tool calls (C1): no producer sends
	// a reasoning duration, so the wall time between the first delta and
	// whatever event settles the block is what the reader actually
	// waited. Zero while nothing is pending.
	pendingStartedAt time.Time

	// hideReasoning collapses every live reasoning block. It is a view
	// state, not a filter: the blocks stay in the window and in the ring.
	hideReasoning bool

	// spinnerFrame mirrors the statusline's own tick (SetSpinnerFrame),
	// the frame a RUNNING tool block's column-1 spinner draws
	// (C3). The transcript arms no clock of
	// its own for it - see .agents/memories/tui-spinner-clock-*.md.
	spinnerFrame int

	// Mouse selection (selection.go): the absolute rect the owning
	// screen injects at layout, and the anchor/focus pair the router
	// drives during a drag. Plain structs, safe under this model's
	// value-copy discipline.
	selRect  sel.Rect
	selState sel.Selection

	// Now is the clock the transcript times tool calls against. It is a
	// field so tests can freeze it; nil means time.Now.
	//
	// The UI has to do this timing itself: uievent.ToolEndBody carries a
	// DurationMS, but no producer sets it (uiadapter.translateToolEnd and
	// thread.LoadHistory are the only two, and agent.Event has no
	// duration to give them), so every tool header rendered "0ms" and the
	// work row's cost was always dropped. What is measured here is the
	// wall time between this transcript seeing the start event and seeing
	// the end event, which is the interval the person watching the screen
	// actually waited. A DurationMS the producer does supply still wins.
	Now func() time.Time

	// turnStartedAt is stamped when the transcript sees turn.start and is
	// used to supply the wall-time portion of the usage footer.
	turnStartedAt time.Time
	modelName     string

	// stream caches the rendered prefix of the in-flight text-delta
	// span (C6). It is a POINTER on purpose: Model is copied by value on
	// every HandleEvent (see the pending field's own comment), and the
	// whole point of the cache is to survive across that copy chain so
	// the same growing buffer is not rendered from scratch on every
	// flush tick - a copy of the pointer still refers to the one
	// renderer for this span. clearPending resets it whenever a span
	// ends, so the NEXT span starts from a clean cache rather than
	// inheriting one built for different text.
	stream *render.StreamRenderer
}

// now reads the transcript's clock.
func (m Model) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// New returns an empty Model with no block focused, following the tail.
func New(t theme.Theme, tier theme.Tier) Model {
	return Model{Theme: t, Tier: tier, focus: -1, follow: true}
}

// Empty reports whether the transcript has no conversation blocks and no active streaming tail.
func (m Model) Empty() bool { return len(m.blocks) == 0 && m.pending == "" }

// SetModel updates the model name shown on subsequently pushed usage footers.
func (m *Model) SetModel(name string) { m.modelName = name }

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
		return m.handleReasoningDelta(b)
	case uievent.TextEndBody:
		return m.handleTextEnd(b)
	case uievent.AssistantResetBody:
		return m.handleAssistantReset(b)
	case uievent.TurnStartBody:
		m = m.flushPending()
		m.turnStartedAt = m.now()
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
		// Deliberately NOT flushPending, unlike every sibling arm here:
		// text.end re-sends the whole answer, so committing the partial
		// span would render it twice. See
		// TestMidStreamNoticeDoesNotDuplicateTheAnswer.
		return m.pushBlock(noticeBlockValue(b))
	case uievent.HookBody:
		m = m.flushPending()
		return m.pushBlock(hookBlockValue(b))
	case uievent.ErrorBody:
		m = m.flushPending()
		return m.pushBlock(errorBlockValue(b))
	case uievent.UsageBody:
		elapsed := b.ElapsedSeconds
		if !m.turnStartedAt.IsZero() {
			elapsed = m.now().Sub(m.turnStartedAt).Seconds()
			if elapsed < 0 {
				elapsed = 0
			}
		}
		return m.pushBlock(usageBlockValue(m.Theme, m.Tier, b, m.modelName, elapsed))
	case uievent.TurnEndBody:
		return m.handleTurnEndEvent(b)
	}
	return m, nil
}

// handleTextEnd is HandleEvent's TextEndBody arm, split out to keep
// HandleEvent itself under the file's per-function LOC cap.
func (m Model) handleTextEnd(b uievent.TextEndBody) (Model, tea.Cmd) {
	// A pending REASONING span must be flushed, not discarded. text.end
	// carries the answer, which says nothing about the reasoning that
	// preceded it, so discarding here wiped the whole reasoning block of
	// any agent that reasoned and then answered with no tool call in
	// between - the exact shape of a subagent run.
	//
	// Only reasoning is flushed. A pending TEXT span is already contained
	// in this event's own Text (the loop sends the full accumulated
	// answer), so flushing that would render the answer twice.
	if m.pendingKind == uievent.KindReasoning {
		m = m.flushPending()
	}
	m.clearPending()
	if b.Text == "" {
		return m, nil
	}
	return m.pushBlock(Block{
		Kind:  uievent.KindTextEnd,
		Prose: true,
		Input: b.Text,
		Body:  proseLines(render.Markdown(m.Theme, m.Tier, m.proseRenderWidth(), b.Text)),
	})
}

// handleTurnEndEvent is HandleEvent's TurnEndBody arm, split out to keep
// HandleEvent itself under the file's per-function LOC cap.
func (m Model) handleTurnEndEvent(b uievent.TurnEndBody) (Model, tea.Cmd) {
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
	// clearPending, not an inlined field reset: it also resets m.stream
	// (the streaming-markdown cache), which every OTHER span-ending path
	// already goes through. Inlining the reset here once let it drift
	// out of sync with that invariant - Clear() zeroed pending/
	// pendingKind/pendingStartedAt/flushWait but left m.stream's cached
	// prefix (and a poisoned flag, if the span had one) pointing at the
	// wiped conversation, silently reintroducing the O(n^2) render cost
	// StreamRenderer exists to remove for the rest of whatever streams
	// next. /clear is accepted mid-turn (uiadapter's handleClear does
	// not check s.active) and the turn keeps emitting deltas afterward
	// (chat.Session.resetSystem invalidates the history writeback, not
	// the running turn), so this path is reachable in practice, not
	// just in theory. Found by bug-audit; regression:
	// TestClearResetsStreamCache.
	m.clearPending()
	return m
}

// turnReasonCompleted is the one reason that commits no block.
const turnReasonCompleted = "completed"

// endTurnUnfinished flushes any partial stream as prose, then records
// why the turn stopped.
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
		Kind: uievent.KindToolPending, CallID: b.ToolCallID, Args: b.Args,
		StartedAt: m.now(),
		Header: Header{
			Label: b.Name, Detail: render.FormatToolDetail(b.Name, b.Args),
			State: "pending", Role: theme.RoleWarning,
		},
	})
}

func (m Model) handleToolStart(b uievent.ToolStartBody) (Model, tea.Cmd) {
	if ok := m.updateLive(b.ToolCallID, func(blk *Block) {
		blk.Kind = uievent.KindToolStart
		if len(b.Args) > 0 {
			blk.Args = b.Args
		}
		blk.Header.State, blk.Header.Role = "running", theme.RoleInfo
		// StartedAt is deliberately NOT touched here. The pending and
		// push paths both stamp it, and a call announced as pending was
		// first seen then - so overwriting it now would discard the
		// queued wait, which is part of what the reader sat through.
		if d := render.FormatToolDetail(b.Name, b.Args); d != "" {
			blk.Header.Detail = d
		}
	}); ok {
		return m, nil
	}
	return m.pushBlock(Block{
		Kind: uievent.KindToolStart, CallID: b.ToolCallID, Args: b.Args,
		StartedAt: m.now(),
		Header: Header{
			Label: b.Name, Detail: render.FormatToolDetail(b.Name, b.Args),
			State: "running", Role: theme.RoleInfo,
		},
	})
}

func (m Model) handleToolOutput(b uievent.ToolOutputBody) (Model, tea.Cmd) {
	// Subagent progress (heartbeat elapsed/tool-call/step counters) is the
	// sidebar panel's job now (internal/ui/screen/conversation/filespanel.go
	// renders it live, computed at render time). It used to live-rewrite
	// this tool call's block on every heartbeat - a churning
	// "elapsed=Xs steps=N" line replacing itself in the middle of the
	// scrollback for the life of a long-running subagent. The block pushed
	// by handleToolStart (name, args, "running") now stays static until
	// handleToolEnd renders its terminal state.
	if b.Progress != nil {
		return m, nil
	}
	lines := outputLines(b)
	if len(lines) == 0 {
		return m.pushBlock(toolOutputBlock(m.Theme, m.Tier, b))
	}
	if ok := m.updateLive(b.ToolCallID, func(blk *Block) {
		blk.Body = append(slices.Clone(blk.Body), lines...)
	}); ok {
		return m, nil
	}
	return m.pushBlock(toolOutputBlock(m.Theme, m.Tier, b))
}

func (m Model) handleToolEnd(b uievent.ToolEndBody) (Model, tea.Cmd) {
	w := m.diffContentWidth()
	var existingArgs map[string]any
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if m.blocks[i].CallID == b.ToolCallID && len(m.blocks[i].Args) > 0 {
			existingArgs = m.blocks[i].Args
			break
		}
	}
	end := toolEndBlockValue(m.Theme, m.Tier, w, b, existingArgs)
	if ok := m.updateLive(b.ToolCallID, func(blk *Block) {
		blk.Kind = uievent.KindToolEnd
		// The producer's duration wins; otherwise use the interval this
		// transcript actually observed. Without the fallback every
		// header reads "0ms", because no producer sets DurationMS.
		blk.ElapsedMS = end.ElapsedMS
		if blk.ElapsedMS == 0 && !blk.StartedAt.IsZero() {
			blk.ElapsedMS = int(m.now().Sub(blk.StartedAt) / time.Millisecond)
		}
		// Carry the raw diff onto the live block, not just its rendered
		// lines: a merged block that keeps only the rendering cannot be
		// re-rendered when the theme changes, which is the whole point of
		// preserving the payload.
		blk.Diff = end.Diff
		// The end block's formatted summary (ledger ref · size · paging
		// state) outranks the start block's argument echo: the summary is
		// what a reader needs without expanding (ux-rules.md
		// 12.5). Only when the end carries no detail does the start's
		// survive, and never over a diff path.
		startDetail := blk.Header.Detail
		blk.Header = end.Header
		if blk.Header.Detail == "" && startDetail != "" && b.Diff == nil {
			blk.Header.Detail = startDetail
		}
		// end.Header's meta was formatted from the producer's duration.
		// When that was absent and the interval was measured here
		// instead, the meta has to say the measured number.
		if blk.ElapsedMS != end.ElapsedMS {
			blk.Header.Meta = render.FormatElapsed(blk.ElapsedMS)
		}
		if b.Diff != nil {
			// DiffBodyPrefixLen, captured BEFORE the append: restyle
			// needs to know exactly how many of these lines precede the
			// diff, because it cannot re-derive that by comparing
			// lengths once DiffSplit can change the diff's OWN rendered
			// line count (see the field's doc comment).
			blk.DiffBodyPrefixLen = len(blk.Body)
			// blk.DiffSplit, not false: this is a live block merging its
			// first diff in, and DiffSplit's zero value is already
			// unified - but reading it here (rather than hardcoding
			// false) keeps this site correct if a future event ever
			// lets a running block carry an earlier toggle.
			blk.Body = append(slices.Clone(blk.Body), render.FormatDiffLines(m.Theme, m.Tier, w, *b.Diff, blk.DiffSplit)...)
		} else if len(end.Body) > 0 {
			blk.Body = append(slices.Clone(blk.Body), end.Body...)
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

// SetSpinnerFrame records the statusline's current spinner tick. The
// caller already owns the one shared clock (armTick/disarmTick in
// internal/ui/screen/conversation); this only lets a running tool
// block's column-1 glyph catch up to it on the next render.
func (m *Model) SetSpinnerFrame(frame int) {
	m.spinnerFrame = frame
}

// SetTheme records a theme change and rebuilds every block body that was
// styled when it was pushed. A body that is not rebuilt here keeps the
// previous theme's colours on screen until a new event replaces it.
func (m *Model) SetTheme(t theme.Theme, tier theme.Tier) {
	m.Theme, m.Tier = t, tier
	m.blocks = slices.Clone(m.blocks)
	for i := range m.blocks {
		m.blocks[i] = m.restyle(m.blocks[i])
	}
}

// restyle rebuilds one block from the raw payload it preserved.
//
// It keys off the payload, not the Kind, for the tool arms: one tool
// call's block advances pending -> start -> end as it runs, and a Kind
// switch stops re-rendering the moment a block carries a payload under
// a Kind the switch does not list.
func (m Model) restyle(b Block) Block {
	switch {
	case b.Kind == uievent.KindTurnStart && b.Input != "":
		b.Body = userLines(m.Theme, m.Tier, m.width, b.Input)
	case b.Kind == uievent.KindTextEnd && b.Input != "":
		b.Body = proseLines(render.Markdown(m.Theme, m.Tier, m.proseRenderWidth(), b.Input))
	case b.Plan != nil:
		// Rebuild the body, not the whole block: the block's identity and
		// its collapse/focus state are the reader's, not the payload's.
		next := planBlockValue(m.Theme, m.Tier, *b.Plan)
		b.Header, b.Body = next.Header, next.Body
	case b.Usage != nil:
		// Same payload-preserving rebuild as the plan: the footer line is
		// styled at push time, so the theme change must restyle it.
		next := usageBlockValue(m.Theme, m.Tier, *b.Usage, b.UsageModel, float64(b.UsageElapsedMS)/1000)
		b.Body = next.Body
	case b.Diff != nil:
		w := m.diffContentWidth()
		// b.DiffSplit, not false: restyle is the toggle's own re-render
		// path (ToggleFocusedDiffSplit flips DiffSplit then calls this),
		// as well as the theme/width-change path - either way the
		// EXISTING split state must survive the rebuild, not reset to
		// unified.
		//
		// Reslice by b.DiffBodyPrefixLen, not by matching the OLD and
		// NEW diff renders' lengths (replaceDiffTail's approach, until
		// bug-audit found it corrupting the body): unified and split do
		// not render a balanced hunk to the same row count, so the
		// moment DiffSplit toggles, "keep body[:len(body)-len(newDiff)]"
		// keeps the wrong number of lines - stale old-diff content, or a
		// duplicated hunk header, depending on which direction the
		// count changed. The prefix length is captured once, when the
		// diff first joins the body (handleToolEnd), and never needs
		// inferring again.
		prefix := b.DiffBodyPrefixLen
		if prefix > len(b.Body) {
			prefix = 0 // defensive: a corrupted prefix must not panic the slice below
		}
		b.Body = append(slices.Clone(b.Body[:prefix]), render.FormatDiffLines(m.Theme, m.Tier, w, *b.Diff, b.DiffSplit)...)
	}
	return b
}

// proseRenderWidth is the wrap width the Glamour-backed markdown
// renderer gets at every prose block: terminal width minus two, with a
// hard floor of 20 columns so a 0-width or under-measured terminal
// never asks Glamour for a 0-column wrap. The two columns are the
// bullet rail that block.BodyRows reserves in the prose path; the floor
// matches render.Markdown's own width guard so the contract has only
// one place to read.
func (m Model) proseRenderWidth() int {
	if m.width <= 0 {
		return 20
	}
	if m.width-2 < 20 {
		return 20
	}
	return m.width - 2
}

// diffContentWidth is the width a diff block actually renders at - the
// SAME arithmetic handleToolEnd and restyle already use
// (m.width-groupIndent-uikitconfig.BodyIndent), centralized here so a
// width check has exactly one place to read (matching
// approval.Model.diffContentWidth's own rationale). Checking m.width
// directly instead of this let ToggleFocusedDiffSplit report success at
// a viewport width in [120,125] while restyle's actual render width
// ([114,119]) was still below render.MinSplitDiffWidth - the toggle
// flipped DiffSplit and returned true, but the rendered Body stayed
// unified, and a LATER unrelated resize could then silently flip the
// render to split with no further key press. Found by bug-audit.
func (m Model) diffContentWidth() int {
	w := m.width - groupIndent - uikitconfig.BodyIndent
	if w <= 0 {
		return 80
	}
	return w
}
