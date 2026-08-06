package agenttools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

func buildStatusView(ctx context.Context, repo workflowledger.Repository, runID string) (StatusView, error) {
	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		if errors.Is(err, workflowledger.ErrNotFound) {
			return StatusView{}, fmt.Errorf("workflow run %q not found", runID)
		}
		return StatusView{}, err
	}
	view := StatusView{
		RunID:      run.RunID,
		Workflow:   run.WorkflowName,
		Status:     string(run.Status),
		ActiveStep: run.ActiveStepID,
		Version:    run.Version,
		StartedAt:  formatTime(run.StartedAt),
		DeadlineAt: formatTimePtr(run.DeadlineAt),
		FinishedAt: formatTimePtr(run.FinishedAt),
		BaseRef:    run.BaseRef,
		BaseCommit: run.BaseCommit,
		Worktree:   run.WorktreeName,
		Attempts:   []AttemptView{},
	}
	attempts, err := repo.ListStepAttempts(ctx, runID)
	if err != nil {
		return StatusView{}, err
	}
	for _, a := range attempts {
		av := AttemptView{
			Step:             a.StepID,
			Attempt:          a.AttemptNo,
			Status:           string(a.Status),
			ToStep:           a.ToStepID,
			OutputDigest:     a.OutputDigest,
			OutputRef:        a.OutputRef,
			CoordinatorRunID: a.CoordinatorRunID,
			TaskID:           a.TaskID,
			MatchDigest:      a.MatchDigest,
		}
		if v := extractVerdict(a); v != "" {
			av.Verdict = v
		}
		view.Attempts = append(view.Attempts, av)
	}
	counters, err := repo.GetLoopCounters(ctx, runID)
	if err != nil {
		return StatusView{}, err
	}
	for _, c := range counters {
		view.Loops = append(view.Loops, LoopView{Name: c.LoopName, Iterations: c.Iterations})
	}
	approvals, err := repo.ListApprovals(ctx, runID)
	if err != nil {
		return StatusView{}, err
	}
	for _, a := range approvals {
		view.Approvals = append(view.Approvals, ApprovalView{
			ApprovalID: a.ApprovalID,
			Step:       a.StepID,
			Status:     a.Status,
			Actor:      a.Actor,
			Reason:     a.Reason,
		})
	}
	deliveries, err := repo.ListDeliveries(ctx, runID)
	if err != nil {
		return StatusView{}, err
	}
	for _, d := range deliveries {
		view.Delivery = append(view.Delivery, DeliveryView{
			IdempotencyKey: d.IdempotencyKey,
			Status:         d.Status,
			Mode:           d.Mode,
			URL:            d.URL,
			CommitSHA:      d.CommitSHA,
			ErrorRef:       d.ErrorRef,
		})
	}
	return view, nil
}

// extractVerdict pulls a gate verdict from decision JSON or stored output fields
// without loading full output bodies into the status envelope.
func extractVerdict(a workflowledger.StepAttempt) string {
	if len(a.DecisionJSON) == 0 {
		return ""
	}
	var decision map[string]any
	if err := json.Unmarshal(a.DecisionJSON, &decision); err != nil {
		return ""
	}
	// Prefer explicit selected output fields from the matcher decision.
	if selected, ok := decision["selected"].(map[string]any); ok {
		if output, ok := selected["output"].(map[string]any); ok {
			if v, ok := output["verdict"].(string); ok {
				return v
			}
		}
		if v, ok := selected["verdict"].(string); ok {
			return v
		}
	}
	if v, ok := decision["verdict"].(string); ok {
		return v
	}
	return ""
}

func buildInspectView(ctx context.Context, repo workflowledger.Repository, runID string, attempt workflowledger.StepAttempt) (InspectView, error) {
	view := InspectView{
		RunID:            runID,
		Step:             attempt.StepID,
		Attempt:          attempt.AttemptNo,
		Status:           string(attempt.Status),
		CoordinatorRunID: attempt.CoordinatorRunID,
		TaskID:           attempt.TaskID,
		OutputRef:        attempt.OutputRef,
		OutputDigest:     attempt.OutputDigest,
	}
	if len(attempt.EvidenceJSON) > 0 {
		var evidence any
		if err := json.Unmarshal(attempt.EvidenceJSON, &evidence); err == nil {
			view.EvidenceSelection = evidence
		}
	}
	if attempt.ToStepID != "" || attempt.MatchDigest != "" || len(attempt.DecisionJSON) > 0 {
		tv := &TransitionView{
			Index:       attempt.TransitionIndex,
			ToStep:      attempt.ToStepID,
			MatchDigest: attempt.MatchDigest,
		}
		if len(attempt.DecisionJSON) > 0 {
			var decision map[string]any
			if err := json.Unmarshal(attempt.DecisionJSON, &decision); err == nil {
				if selected, ok := decision["selected"].(map[string]any); ok {
					tv.Selected = selected
				} else {
					tv.Selected = decision
				}
			}
		}
		view.Transition = tv
	}
	if attempt.OutputRef != "" {
		data, err := repo.LoadContent(ctx, attempt.OutputRef)
		if err != nil && !errors.Is(err, workflowledger.ErrContentNotFound) {
			return InspectView{}, err
		}
		if err == nil && len(data) > 0 {
			var output any
			if json.Unmarshal(data, &output) == nil {
				view.Output = output
			} else {
				// Non-JSON output is returned as a string; raw prompts stay out
				// by construction (attempt outputs are schema-validated JSON).
				view.Output = string(data)
			}
		}
	}
	return view, nil
}
