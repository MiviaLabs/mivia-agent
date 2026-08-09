package agent

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

func executeToolTask(idx int, task *toolTask, reg *tools.Registry, scheduler *toolScheduler, opts Options, results []toolExecResult, finished *atomic.Int32) {
	if _, ok := reg.Get(task.call.Function.Name); !ok {
		if opts.StagedToolMessage != nil {
			if msg, ok := opts.StagedToolMessage(task.call.Function.Name); ok {
				failToolTask(idx, task, opts, results, finished, fmt.Errorf("%s", msg))
				return
			}
		}
		failToolTask(idx, task, opts, results, finished, fmt.Errorf("tool %q is not available to this agent", task.call.Function.Name))
		return
	}
	release, err := scheduler.acquire(task.callCtx, task.capability.ResourceKey)
	if err != nil {
		failToolTask(idx, task, opts, results, finished, err)
		return
	}
	execCtx, cancelExec := context.WithTimeout(task.callCtx, task.timeout)
	defer cancelExec()
	if err := execCtx.Err(); err != nil {
		release()
		failToolTask(idx, task, opts, results, finished, err)
		return
	}
	emit(opts, Event{Kind: EventToolStart, ToolCallID: task.call.ID, Name: task.call.Function.Name, Detail: "running"})
	r := opts.Dispatcher.Invoke(execCtx, runtime.Request{
		ID: task.call.ID, ParentID: opts.ParentID, TurnID: opts.TurnID, Step: task.step,
		SkipDedup: task.skipDedup, SessionID: opts.SessionID, Role: opts.Role,
		Depth: opts.Depth, Budget: opts.Budget, Kind: runtime.Tool, Name: task.call.Function.Name,
		Input: task.raw, Timeout: task.timeout,
	})
	release()
	results[idx] = buildExecResult(idx, task, reg, opts, r)
	emitToolEnd(opts, results[idx])
	emitHookRuns(opts, task.call.ID, r.HookRuns)
	if finished != nil {
		finished.Add(1)
	}
}
