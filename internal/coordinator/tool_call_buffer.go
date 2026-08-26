package coordinator

import (
	"sync"

	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// runToolCallBuffer is a per-run, mutex-guarded, per-task-capped buffer of
// RAW subagents.ToolCallStep lifecycle events (start+end, unmerged —
// merging into one-row-per-call happens later, at envelope-encode time,
// not here). It is ledger-only content, never handed to the model or UI
// directly, so its caps are generous relative to the model/UI-visible
// envelope layer that will be added in a later chunk.
type runToolCallBuffer struct {
	mu    sync.Mutex
	steps map[string][]subagents.ToolCallStep // taskID -> raw steps
	bytes map[string]int                      // taskID -> running byte total (len(Input)+len(Output))
}

const (
	// bufferMaxStepsPerTask caps raw lifecycle events (start+end,
	// unmerged) retained per task before further events are dropped
	// wholesale (never a half-written event).
	//
	// bufferMaxStepsPerTask and bufferMaxBytesPerTask are kept comfortably
	// larger than 2x the envelope layer's per-task complete-call cap (added
	// in a later chunk) so the chunk-8 boundary regression test has real
	// margin; if you lower either, check that test.
	bufferMaxStepsPerTask = 200
	// bufferMaxBytesPerTask caps total Input+Output bytes retained per
	// task. Ledger storage is cheap and this content is never model- or
	// UI-visible directly, so this is deliberately generous relative to
	// the envelope layer's per-field bound.
	bufferMaxBytesPerTask = 64 * 1024
)

func newRunToolCallBuffer() *runToolCallBuffer {
	return &runToolCallBuffer{
		steps: make(map[string][]subagents.ToolCallStep),
		bytes: make(map[string]int),
	}
}

// sinkFor returns a closure bound to one taskID (captured, not carried on
// ToolCallStep — ToolCallStep never needs a TaskID field because each
// task's sink is installed on that task's own per-task context by
// contextForTask, not shared/demuxed centrally).
func (b *runToolCallBuffer) sinkFor(taskID string) subagents.ToolCallSink {
	return func(step subagents.ToolCallStep) {
		b.mu.Lock()
		defer b.mu.Unlock()
		stepBytes := len(step.Input) + len(step.Output)
		if len(b.steps[taskID]) >= bufferMaxStepsPerTask {
			return
		}
		if b.bytes[taskID]+stepBytes > bufferMaxBytesPerTask {
			return
		}
		b.steps[taskID] = append(b.steps[taskID], step)
		b.bytes[taskID] += stepBytes
	}
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
	return steps
}
