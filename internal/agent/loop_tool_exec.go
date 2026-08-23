package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// approvalClassThreshold is the minimum ExecutionClass that prompts.
// ExecutionRead (0) and Unclassified tools run without asking. The
// constant is local to this file because only the legacy executeToolTask
// path consults it; the SDK-path wrapper carries the same threshold via
// the SDK's tools.ExecutionClassRank.
var approvalClassThreshold = tools.ExecutionWrite

// approvalClassName returns a stable, lowercase string for the
// execution class without depending on a String() method (none exists
// today). The string rides on Event.Detail so downstream consumers
// (the uiadapter translator) can route without re-deriving from the
// registry.
func approvalClassName(c tools.ExecutionClass) string {
	switch c {
	case tools.ExecutionRead:
		return "read"
	case tools.ExecutionWrite:
		return "write"
	case tools.ExecutionExternal:
		return "external"
	default:
		return "unclassified"
	}
}

// approvalDenied is the message rendered to the model when the user
// denies a tool call. The text mirrors the legacy staged-tool denial
// shape: a short prefix + the user's verbatim decision so the model can
// tell the call was rejected, not lost.
const approvalDenied = "tool call denied by user: %s"

// gateToolApproval runs the approval gate for one tool task. It
// prompts for any tool whose capability.Class >=
// approvalClassThreshold; lower-class tools (Read / Unclassified)
// skip the gate entirely. A nil ApprovalGate is the pre-Phase-4
// behavior: every tool runs without prompting. The standing cache is
// consulted BEFORE the gate so an "always" decision short-circuits
// the prompt for the rest of the session. It returns false when the
// task was failed (denied or canceled) and must not proceed.
func gateToolApproval(idx int, task *toolTask, opts Options, results []toolExecResult, finished *atomic.Int32) bool {
	if task.capability.Class < approvalClassThreshold || opts.ApprovalGate == nil {
		return true
	}
	approved, standingHit := true, false
	if opts.ApprovalStanding != nil {
		if v, ok := opts.ApprovalStanding.Lookup(task.call.Function.Name); ok {
			approved, standingHit = v, true
		}
	}
	if standingHit {
		if approved {
			return true
		}
		failToolTask(idx, task, opts, results, finished, fmt.Errorf(approvalDenied, "denied by standing decision"))
		return false
	}
	emit(opts, Event{Kind: EventToolPending, ToolCallID: task.call.ID, Name: task.call.Function.Name, Detail: approvalClassName(task.capability.Class), Input: string(task.raw)})
	res := opts.ApprovalGate(task.callCtx, task.call.Function.Name, json.RawMessage(task.raw))
	approved = res.Approved
	if res.ApprovedForClass && opts.ApprovalStanding != nil {
		if res.Approved {
			opts.ApprovalStanding.Allow(task.call.Function.Name, task.capability.Class)
		} else {
			opts.ApprovalStanding.Deny(task.call.Function.Name, task.capability.Class)
		}
	}
	if !approved {
		errText := res.Err
		if errText == "" {
			errText = "denied"
		}
		failToolTask(idx, task, opts, results, finished, fmt.Errorf(approvalDenied, errText))
		return false
	}
	if errors.Is(task.callCtx.Err(), context.Canceled) {
		failToolTask(idx, task, opts, results, finished, task.callCtx.Err())
		return false
	}
	return true
}

func executeToolTask(idx int, task *toolTask, reg *tools.Registry, scheduler *toolScheduler, opts Options, results []toolExecResult, finished *atomic.Int32) {
	if _, ok := reg.Get(task.call.Function.Name); !ok {
		if opts.StagedToolMessage != nil {
			if msg, ok := opts.StagedToolMessage(task.call.Function.Name); ok {
				failToolTask(idx, task, opts, results, finished, fmt.Errorf("%s", msg))
				return
			}
		}
		if opts.UnadmittedToolHandler != nil {
			if msg, ok := opts.UnadmittedToolHandler(task.callCtx, task.call.Function.Name); ok {
				failToolTask(idx, task, opts, results, finished, fmt.Errorf("%s", msg))
				return
			}
		}
		failToolTask(idx, task, opts, results, finished, fmt.Errorf("tool %q is not available to this agent", task.call.Function.Name))
		return
	}
	// Approval gate: see gateToolApproval for the full contract.
	if !gateToolApproval(idx, task, opts, results, finished) {
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
