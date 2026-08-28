package coordinator

import (
	"sync"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// runToolCallBuffer is a per-run, mutex-guarded, per-task-capped buffer of
// RAW subagents.ToolCallStep lifecycle events (start+end, unmerged —
// merging into one-row-per-call happens later, at envelope-encode time,
// not here). It is ledger-only content, never handed to the model or UI
// directly, so its caps are generous relative to the model/UI-visible
// envelope layer's cliorchestrate.envelopeMaxToolCallPairs (20 complete,
// merged calls) - see loadToolCallSummaries in dispatch_encode.go.
type runToolCallBuffer struct {
	mu      sync.Mutex
	steps   map[string][]subagents.ToolCallStep // taskID -> raw steps
	bytes   map[string]int                      // taskID -> running byte total (len(Input)+len(Output))
	dropped map[string]map[string]bool          // taskID -> set of ToolCallIDs with >=1 raw event dropped by a cap
	// progress is the cap-proof liveness view behind RunHandle.TaskProgress:
	// updated on EVERY sink event before the caps are consulted, so a chatty
	// task's counters never go stale the way the capped raw buffer would.
	progress map[string]*TaskProgress
}

const (
	// bufferMaxStepsPerTask caps raw lifecycle events (start+end,
	// unmerged) retained per task before further events are dropped
	// wholesale (never a half-written event).
	//
	// bufferMaxStepsPerTask and bufferMaxBytesPerTask are kept comfortably
	// larger than 2x envelopeMaxToolCallPairs (20, so >40 raw start+end
	// events for a full complement of complete calls) so the buffer layer's
	// cap remains the real headroom, not the envelope layer's, and so the
	// chunk-8 boundary regression tests (TestLoadToolCallSummaries*, in
	// internal/cliorchestrate/dispatch_encode_test.go) have real margin - if
	// you lower either constant, or envelopeMaxToolCallPairs, re-check those
	// tests.
	bufferMaxStepsPerTask = 200
	// bufferMaxBytesPerTask caps total Input+Output bytes retained per
	// task. Ledger storage is cheap and this content is never model- or
	// UI-visible directly, so this is deliberately generous relative to
	// the envelope layer's per-field bound.
	bufferMaxBytesPerTask = 64 * 1024
)

func newRunToolCallBuffer() *runToolCallBuffer {
	return &runToolCallBuffer{
		steps:    make(map[string][]subagents.ToolCallStep),
		bytes:    make(map[string]int),
		dropped:  make(map[string]map[string]bool),
		progress: make(map[string]*TaskProgress),
	}
}

// TaskProgress is the cap-proof per-task liveness view behind
// RunHandle.TaskProgress: counters updated on EVERY sink event regardless of
// the raw buffer's caps, so a chatty task never reads stale the way the
// capped raw steps would. Zero value = no tool activity observed yet - on a
// long-running task that zero is itself the wedge signal (dispatched, never
// reached its first tool call).
type TaskProgress struct {
	ToolCalls    int       // count of tool-call START events, dropped or not
	LastTool     string    // most recent tool name (start or end)
	LastActivity time.Time // most recent sink event, any kind
}

// sinkFor returns a closure bound to one taskID (captured, not carried on
// ToolCallStep — ToolCallStep never needs a TaskID field because each
// task's sink is installed on that task's own per-task context by
// contextForTask, not shared/demuxed centrally).
//
// Once any raw event for a given ToolCallID is dropped by either cap, that
// ToolCallID is "poisoned" for the rest of this buffer's lifetime for this
// task: every subsequent event for it is dropped too, even one that would
// otherwise fit under the byte budget on its own marginal size. This keeps a
// call's raw trace atomic — fully present, fully absent, or correctly
// start-only (which loadToolCallSummaries reports Incomplete: true) — and
// never "end with no start", which would merge into a false-completeness
// summary (real Output, empty Name/Input, Incomplete: false).
func (b *runToolCallBuffer) sinkFor(taskID string) subagents.ToolCallSink {
	if b == nil {
		return func(subagents.ToolCallStep) {}
	}
	return func(step subagents.ToolCallStep) {
		b.mu.Lock()
		defer b.mu.Unlock()
		// Progress counts the event BEFORE any cap verdict: the tool call
		// really ran whether or not its raw trace fits the buffer, and
		// last-activity must track the newest event, which is exactly the
		// one a full buffer would drop.
		if b.progress == nil {
			b.progress = make(map[string]*TaskProgress)
		}
		p := b.progress[taskID]
		if p == nil {
			p = &TaskProgress{}
			b.progress[taskID] = p
		}
		if step.Kind == "start" {
			p.ToolCalls++
		}
		p.LastTool = step.Name
		p.LastActivity = step.At
		if b.dropped[taskID][step.ToolCallID] {
			return
		}
		stepBytes := len(step.Input) + len(step.Output)
		capped := len(b.steps[taskID]) >= bufferMaxStepsPerTask ||
			b.bytes[taskID]+stepBytes > bufferMaxBytesPerTask
		if capped {
			if b.dropped[taskID] == nil {
				b.dropped[taskID] = make(map[string]bool)
			}
			b.dropped[taskID][step.ToolCallID] = true
			return
		}
		b.steps[taskID] = append(b.steps[taskID], step)
		b.bytes[taskID] += stepBytes
	}
}

// reset clears any leftover buffered steps for one taskID, with no effect on
// other tasks' entries. Called by contextForTask at the start of every
// dispatch attempt (first attempt or retry redispatch) so a retried task's
// discarded prior-attempt steps never bleed into the final flush (Finding 1,
// Part B hostile bug audit): the buffer is keyed only by taskID and flushed
// only on the terminal result, so without this reset a retryable failure's
// buffered steps would otherwise persist across the retry's redispatch.
// Deliberately leaves progress untouched: those counters describe the task's
// lifetime activity (liveness for inspect_agents), not one attempt's trace.
// Nil-safe and safe to call on an already-empty slot (the common case: the
// first attempt of a task that will never retry).
func (b *runToolCallBuffer) reset(taskID string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.steps, taskID)
	delete(b.bytes, taskID)
	delete(b.dropped, taskID)
}

// progressSnapshot returns a copy of the per-task liveness view. Nil-safe:
// a nil *runToolCallBuffer (a RunHandle built by hand in a test) returns nil.
func (b *runToolCallBuffer) progressSnapshot() map[string]TaskProgress {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make(map[string]TaskProgress, len(b.progress))
	for id, p := range b.progress {
		if p != nil {
			out[id] = *p
		}
	}
	return out
}

// flush pops and clears one task's buffered raw steps. Nil-safe: a nil
// *runToolCallBuffer (e.g. a RunHandle built by hand in a test, bypassing
// spawn.go) returns nil with no panic.
func (b *runToolCallBuffer) flush(taskID string) []subagents.ToolCallStep {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	steps := b.steps[taskID]
	delete(b.steps, taskID)
	delete(b.bytes, taskID)
	delete(b.dropped, taskID)
	return steps
}
