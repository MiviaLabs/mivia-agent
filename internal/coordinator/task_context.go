package coordinator

import (
	"context"

	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// runExecKey carries per-run execution metadata on the pool context so
// ContextForTask can stamp TaskIdentity without shared mutable pool state.
type runExecKey struct{}

type runExecInfo struct {
	runID     string
	agents    map[string]string // taskID → agent name
	mailboxes *runMailboxes     // shared with RunHandle (plan 53.03)
}

// contextWithRunExec stamps run coordination metadata onto ctx.
func contextWithRunExec(ctx context.Context, runID string, tasks []subagents.Task, mailboxes *runMailboxes) context.Context {
	agents := make(map[string]string, len(tasks))
	for _, t := range tasks {
		name := t.AgentName
		if name == "" {
			name = t.Name
		}
		agents[t.ID] = name
	}
	return context.WithValue(ctx, runExecKey{}, runExecInfo{runID: runID, agents: agents, mailboxes: mailboxes})
}

func runExecFrom(ctx context.Context) (runExecInfo, bool) {
	info, ok := ctx.Value(runExecKey{}).(runExecInfo)
	return info, ok
}

// contextForTask is installed on the subagent pool. It is pure w.r.t. the
// parent context (no shared pool mutation), so concurrent runs are safe.
func contextForTask(ctx context.Context, taskID string) context.Context {
	info, ok := runExecFrom(ctx)
	runID, agent := "", ""
	if ok {
		runID = info.runID
		agent = info.agents[taskID]
	}
	ctx = runtime.ContextWithTaskIdentity(ctx, runtime.TaskIdentity{
		RunID:  runID,
		TaskID: taskID,
		Agent:  agent,
	})
	if ok && info.mailboxes != nil {
		mb := info.mailboxes
		tid := taskID
		ctx = runtime.ContextWithMailboxDrain(ctx, func() []runtime.ParentMessage {
			raw := mb.Drain(tid)
			out := make([]runtime.ParentMessage, 0, len(raw))
			for _, m := range raw {
				out = append(out, runtime.ParentMessage{
					Kind: string(m.Kind), Body: m.Body, MessageID: m.ID,
				})
			}
			return out
		})
	}
	return ctx
}
