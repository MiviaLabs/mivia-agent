package controller

import (
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"time"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// admissionSnapshot builds the durable run row for a fresh admission. The
// deadline is the admission deadline when present, else the workflow's
// max_duration_seconds counted from the admission time.
func (c *LinearController) admissionSnapshot() workflowledger.RunSnapshot {
	admittedAt := c.now()
	snap := workflowledger.RunSnapshot{
		RunID: c.RunID, InvocationKey: c.admission.InvocationKey, WorkflowName: c.Workflow.Name, WorkflowDigest: c.admittedWorkflowDigest(),
		SnapshotDigest: workflowledger.SnapshotDigest(c.Snapshot), InputDigest: c.admission.InputDigest,
		Status: workflowledger.RunStatusPending, ActiveStepID: c.runStartStepID(),
		BaseRef: c.admission.BaseRef, BaseCommit: c.admission.BaseCommit,
		OriginBaseCommit: c.admission.OriginBaseCommit, WorktreeName: c.admission.WorktreeName,
		RemoteURL: c.admission.RemoteURL, StartedAt: admittedAt,
	}
	if c.admission.DeadlineAt != nil {
		deadline := *c.admission.DeadlineAt
		snap.DeadlineAt = &deadline
	} else if c.Workflow.Limits.MaxDurationSeconds > 0 {
		deadline := admittedAt.Add(config.SaturatingSeconds(c.Workflow.Limits.MaxDurationSeconds))
		snap.DeadlineAt = &deadline
	}
	return snap
}

// admittedWorkflowDigest returns the digest to compare against the stored run:
// the recorded one for a resume, this binary's for a fresh admission. A fresh
// admission keeps the full guard, so two concurrent admissions of different
// workflows still collide.
func (c *LinearController) admittedWorkflowDigest() string {
	if c.admission.WorkflowDigest != "" {
		return c.admission.WorkflowDigest
	}
	return c.Workflow.Digest
}

// sameAdmission compares the immutable admission data of a stored run against
// a fresh admission snapshot.
func sameAdmission(stored, candidate workflowledger.RunSnapshot) bool {
	return stored.InvocationKey == candidate.InvocationKey && stored.WorkflowName == candidate.WorkflowName && stored.WorkflowDigest == candidate.WorkflowDigest &&
		stored.SnapshotDigest == candidate.SnapshotDigest && stored.InputDigest == candidate.InputDigest &&
		stored.BaseRef == candidate.BaseRef && stored.BaseCommit == candidate.BaseCommit &&
		stored.OriginBaseCommit == candidate.OriginBaseCommit &&
		stored.WorktreeName == candidate.WorktreeName && stored.RemoteURL == candidate.RemoteURL &&
		sameDeadline(stored.DeadlineAt, candidate.DeadlineAt)
}

// sameDeadline compares two deadlines for equality; two nil values are equal.
func sameDeadline(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}
