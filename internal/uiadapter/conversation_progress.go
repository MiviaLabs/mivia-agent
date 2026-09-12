// This file (conversation_progress.go) holds the subagent progress registration
// and progress event routing on Conversation: tracking screen ownership of the
// process-wide SubagentProgressRegistrar, per-turn generation tokens, and
// filtering subagent events onto the turn stream. Split out of conversation.go,
// which had crossed the hard per-file line budget.
package uiadapter

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// SubagentProgressRegistrar allows the UI layer to receive live subagent progress events
// (nested tool calls, steps, heartbeats) from the subagent progress callback.
var SubagentProgressRegistrar func(fn func(agent.Event)) (cleanup func())

// beginTurnProgress records the live turn's subagent-progress sink and
// takes the process-wide registrar when this conversation is already the
// one on screen. It returns the generation token this turn just claimed;
// the caller must pass it back to endTurnProgress so a late-running
// release cannot act on behalf of a newer, overlapping turn (see the
// progressGen field doc on Conversation).
//
// A turn that starts off-screen records its handler and installs nothing.
// That is the whole point: the registrar is a single process-wide slot, so
// an unwatched run installing itself would hijack the watched session's
// subagent panel. What changed is that the decision is no longer final -
// syncProgressRegistration re-evaluates it the moment ownership moves.
func (c *Conversation) beginTurnProgress(handler func(agent.Event)) uint64 {
	if c == nil {
		return 0
	}
	c.progressMu.Lock()
	defer c.progressMu.Unlock()
	// A second turn on this conversation must register ITS sink, not keep
	// the finished turn's; releasing under the CURRENT generation here is
	// fine - this call is what makes that generation obsolete, and
	// nothing else can hold the old token.
	c.releaseProgressLocked()
	c.progressHandler = handler
	c.progressGen++
	gen := c.progressGen
	c.acquireProgressLocked()
	return gen
}

// endTurnProgress drops the live turn's sink and releases the registrar,
// but ONLY if gen still names the current turn's acquisition. A turn
// whose beginTurnProgress has been superseded by a newer overlapping
// turn (see runTurnGoroutine's defer-ordering doc, and the progressGen
// field comment) passes a stale gen here; the call is then a deliberate
// no-op rather than tearing down the newer turn's handler and registrar
// hold.
func (c *Conversation) endTurnProgress(gen uint64) {
	if c == nil {
		return
	}
	c.progressMu.Lock()
	defer c.progressMu.Unlock()
	if gen != c.progressGen {
		return
	}
	c.progressHandler = nil
	c.releaseProgressLocked()
}

// syncProgressRegistration brings the registrar in line with who owns the
// screen now. Called on every ownership change, which is what lets a run
// adopted mid-turn start reporting subagent progress.
func (c *Conversation) syncProgressRegistration() {
	if c == nil {
		return
	}
	c.progressMu.Lock()
	defer c.progressMu.Unlock()
	if c.IsForeground() {
		c.acquireProgressLocked()
		return
	}
	c.releaseProgressLocked()
}

// acquireProgressLocked installs this conversation's live handler into the
// process-wide slot. No live turn, no screen ownership, no registrar wired,
// or already held: nothing to do. Callers hold progressMu.
func (c *Conversation) acquireProgressLocked() {
	if c.progressRelease != nil || c.progressHandler == nil {
		return
	}
	if !c.IsForeground() || SubagentProgressRegistrar == nil {
		return
	}
	// The slot is generation-tokened (clichat.SetSubagentProgress), so a
	// later acquisition wins and an older release is a no-op - taking it
	// from another conversation is safe and is what adoption means.
	c.progressRelease = SubagentProgressRegistrar(c.progressHandler)
}

// releaseProgressLocked gives the slot up if this conversation holds it.
// Callers hold progressMu.
func (c *Conversation) releaseProgressLocked() {
	if c.progressRelease == nil {
		return
	}
	c.progressRelease()
	c.progressRelease = nil
}

// subagentForwardKinds lists the uievent.Kind values a non-zero-Origin
// (subagent-authored) agent.Event may still reach the root turn stream
// as, despite the general divert to SubagentThreads.HandleEvent below.
// Only status/lifecycle signals that drive the sidebar panel's row state
// belong here - transcript CONTENT (assistant text, reasoning, nested
// tool-call deltas) must stay diverted to the subagent's own thread,
// which is the whole point of the Origin-based routing newTurnHandler
// does. Today the only producer is translateSubagentDone's tool.output
// entry (event_kind.go), which carries the Progress that
// filespanel.observeAgent keys a subagent's row status on - without it,
// a task's row only ever left "running" via the ENCLOSING dispatch_tasks
// call's own tool.end, so a fast subagent that finished long ago still
// looked stalled until the whole batch resolved. A reviewer must sign
// off before adding another Kind here, the same discipline
// droppedKinds in event.go already requires for the translate switch
// itself.
var subagentForwardKinds = map[uievent.Kind]bool{
	uievent.KindToolOutput: true,
}

// newTurnHandler returns the per-turn agent-event handler that runs as
// the OnAgentEvent tap. It translates each agent.Event via
// uiadapter.TranslateEventWithOptions, stamps the real TurnID once known, and
// forwards onto the channel under a closed-check. Subagent events (non-zero
// Origin) are routed to the SubagentThreads registry as before, AND -
// additively - filtered through subagentForwardKinds and forwarded onto the
// root conversation's turn stream too, so the sidebar panel sees a
// subagent's own completion live instead of only when the whole enclosing
// batch finishes. The select on turnCtx.Done() drops the event rather than
// blocks the agent loop if the buffer is full and Cancel is mid-flight.
func newTurnHandler(stream *turnStream, closed *atomic.Bool, turnIDPtr *atomic.Pointer[string], seq *uint64, turnCtx context.Context, opts TranslateOptions, subagents *SubagentThreads) func(agent.Event) {
	forward := func(translated []uievent.Event) {
		for _, e := range translated {
			if closed.Load() {
				return
			}
			n := atomic.AddUint64(seq, 1)
			if p := turnIDPtr.Load(); p != nil {
				e.TurnID = *p
			}
			e.Seq = n
			e.At = time.Now()
			// One send, serialised against the close. A false result means
			// the stream closed (or the turn ended) under us, which is the
			// same "stop forwarding" signal the closed.Load() check above
			// gives - except this one cannot be stale.
			if !stream.Send(e) {
				return
			}
		}
	}
	return func(ev agent.Event) {
		if closed.Load() {
			return
		}
		if !ev.Origin.IsZero() {
			if subagents != nil {
				subagents.HandleEvent(ev, opts)
			}
			forward(filterSubagentForward(TranslateEventWithOptions(ev, opts)))
			return
		}
		forward(TranslateEventWithOptions(ev, opts))
	}
}

// filterSubagentForward keeps only the status/lifecycle uievent Kinds a
// subagent-origin event is allowed to reach the root turn stream as (see
// subagentForwardKinds). Everything else - transcript content - is
// dropped here so it stays confined to the subagent's own thread, which
// already recorded it via SubagentThreads.HandleEvent.
func filterSubagentForward(translated []uievent.Event) []uievent.Event {
	if len(translated) == 0 {
		return nil
	}
	out := make([]uievent.Event, 0, len(translated))
	for _, e := range translated {
		if subagentForwardKinds[e.Kind] {
			out = append(out, e)
		}
	}
	return out
}
