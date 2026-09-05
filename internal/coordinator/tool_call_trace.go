package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// LoadTaskToolCalls returns the HOST-RECORDED tool-call trace for one task.
//
// This is the authoritative record of what a subagent actually executed: the
// steps come from the agent loop's own tool_start/tool_end events, buffered by
// the coordinator and stored at task completion (persistToolCalls). The child
// agent cannot write to it, unlike the run-message blackboard, which the child
// authors itself through post_message.
//
// An empty result is a legitimate answer - the task made no tool calls, or it
// completed before the trace field existed - and callers must treat it as "no
// executions proven", never as "trust the child".
func (c *coordinator) LoadTaskToolCalls(ctx context.Context, runID, taskID string) ([]subagents.ToolCallStep, error) {
	if runID == "" || taskID == "" {
		return nil, fmt.Errorf("load task tool calls: run_id and task_id are required")
	}
	snap, err := c.repo.GetTask(ctx, runID, taskID)
	if err != nil {
		if errors.Is(err, ledger.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if snap.ToolCallsRef == "" {
		return nil, nil
	}
	data, err := c.repo.LoadContent(ctx, snap.ToolCallsRef)
	if err != nil {
		// A ref the ledger still points at but no longer holds - content GC,
		// a partial restore - is a MISSING record, not a broken store, and
		// reads as "no proven executions" like a task that recorded none.
		// LoadContent signals that with ErrContentNotFound; ErrNotFound is
		// the wrong sentinel here and never matched, so this branch was dead
		// and every missing trace surfaced as a hard error.
		if errors.Is(err, ledger.ErrContentNotFound) {
			return nil, nil
		}
		return nil, err
	}
	var steps []subagents.ToolCallStep
	if err := json.Unmarshal(data, &steps); err != nil {
		return nil, fmt.Errorf("decode task %q tool-call trace: %w", taskID, err)
	}
	return steps, nil
}
