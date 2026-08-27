package cliworkflow

// gate_impl.go owns the stack plan-run delivery gate. It previously lived in
// internal/cli (stack_admit_integration.go) and moved here with the workflow
// domain; internal/cli reaches it through the ClassifyStackPlanRunDeliveryFunc
// and StackPlanRunFailureReasonFunc seams.

import (
	"context"
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// ClassifyStackPlanRunDeliveryImpl reports how runID relates to a multi-chunk
// stack for delivery purposes: not applicable, incomplete (undriven),
// complete, or failed. It mirrors the cli implementation.
func ClassifyStackPlanRunDeliveryImpl(ctx context.Context, root string, store *storage.SQLite, repo workflowledger.Repository, runID string, remoteMergeOracle bool) StackPlanRunGate {
	if snap, err := repo.GetRun(ctx, runID); err == nil && snap.InvocationKey != "" && strings.HasSuffix(snap.InvocationKey, ":"+delivery.IntegrationChunkID) {
		return stackPlanRunNotApplicable
	}
	if _, ok := delivery.DecomposedChunks(ctx, repo, runID); !ok {
		return stackPlanRunNotApplicable
	}
	if failed, _ := StackPlanRunFailureReasonImpl(ctx, root, store, repo, runID); failed {
		return stackPlanRunFailed
	}
	policy := StackPlanMergePolicy(ctx, repo, runID)
	if !StackDriveCompleted(ctx, root, store, repo, runID, policy, remoteMergeOracle) {
		return stackPlanRunIncomplete
	}
	return stackPlanRunComplete
}

// StackPlanRunFailureReasonImpl reports whether a multi-chunk stack plan run
// has already reached a terminal failure state, and why.
func StackPlanRunFailureReasonImpl(ctx context.Context, root string, store *storage.SQLite, repo workflowledger.Repository, runID string) (failed bool, reason string) {
	byID, err := delivery.TaskMap(ctx, workflowledger.NewStore(store), runID)
	if err == nil {
		for taskID, task := range byID {
			if task.Status == delivery.StatusFailed {
				return true, fmt.Sprintf("chunk %s failed terminally", taskID)
			}
			if task.Status == delivery.StatusCanceled {
				return true, fmt.Sprintf("chunk %s was canceled after its dependency failed", taskID)
			}
		}
	}
	intRun, found, err := StackRunRefFunc(repo, runID, delivery.IntegrationChunkID)
	if err == nil && found {
		switch intRun.Status {
		case workflowledger.RunStatusFailed, workflowledger.RunStatusCanceled,
			workflowledger.RunStatusTimedOut:
			return true, fmt.Sprintf("integration run %s is %s", intRun.RunID, intRun.Status)
		}
	}
	return false, ""
}
