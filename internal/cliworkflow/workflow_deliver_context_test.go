package cliworkflow

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
)

type contextCountingGit struct{ calls atomic.Int32 }

func (g *contextCountingGit) Run(context.Context, delivery.GitContext, ...string) (string, error) {
	g.calls.Add(1)
	return "", nil
}

func TestSessionWorkflowEngineDeliverPropagatesCancellation(t *testing.T) {
	root, storePath, config, _ := newDeliveryFixture(t)
	runID := runFixtureToDeliveryPending(t, root, config)
	repo := openDeliveryStore(t, storePath)
	seedWorktreeChange(t, root, runID, repo)

	prevGit := WorkflowDeliverGit
	rec := &contextCountingGit{}
	WorkflowDeliverGit = rec
	t.Cleanup(func() { WorkflowDeliverGit = prevGit })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e := NewSessionWorkflowEngine(root, config)
	_, _ = e.Deliver(ctx, runID, true)
	if got := rec.calls.Load(); got != 0 {
		t.Fatalf("Git received %d calls after cancellation", got)
	}
}
