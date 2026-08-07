package ledger

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"
)

// TestValidRunTransition asserts the run status transition contract.
func TestValidRunTransition(t *testing.T) {
	all := []RunStatus{
		RunStatusPending,
		RunStatusRunning,
		RunStatusWaitingApproval,
		RunStatusDeliveryPending,
		RunStatusSucceeded,
		RunStatusFailed,
		RunStatusCanceled,
		RunStatusTimedOut,
		RunStatusDeliveryFailed,
	}
	accept := []struct{ from, to RunStatus }{
		{RunStatusPending, RunStatusRunning},
		{RunStatusRunning, RunStatusWaitingApproval},
		{RunStatusRunning, RunStatusDeliveryPending},
		{RunStatusRunning, RunStatusSucceeded},
		{RunStatusRunning, RunStatusFailed},
		{RunStatusRunning, RunStatusCanceled},
		{RunStatusRunning, RunStatusTimedOut},
		{RunStatusWaitingApproval, RunStatusRunning},
		{RunStatusWaitingApproval, RunStatusFailed},
		{RunStatusWaitingApproval, RunStatusCanceled},
		{RunStatusWaitingApproval, RunStatusTimedOut},
		{RunStatusDeliveryPending, RunStatusSucceeded},
		{RunStatusDeliveryPending, RunStatusDeliveryFailed},
	}
	for _, tc := range accept {
		t.Run(fmt.Sprintf("accept %s->%s", tc.from, tc.to), func(t *testing.T) {
			if !ValidRunTransition(tc.from, tc.to) {
				t.Errorf("ValidRunTransition(%q, %q) = false, want true", tc.from, tc.to)
			}
		})
	}
	reject := []struct{ from, to RunStatus }{
		{RunStatusPending, RunStatusSucceeded},
		{RunStatusRunning, RunStatusRunning},
		{RunStatusWaitingApproval, RunStatusDeliveryPending},
		{RunStatusDeliveryPending, RunStatusRunning},
	}
	for _, tc := range reject {
		t.Run(fmt.Sprintf("reject %s->%s", tc.from, tc.to), func(t *testing.T) {
			if ValidRunTransition(tc.from, tc.to) {
				t.Errorf("ValidRunTransition(%q, %q) = true, want false", tc.from, tc.to)
			}
		})
	}
	// Terminal statuses reject every destination, including reflexive edges.
	terminals := []RunStatus{
		RunStatusSucceeded,
		RunStatusFailed,
		RunStatusCanceled,
		RunStatusTimedOut,
		RunStatusDeliveryFailed,
	}
	for _, term := range terminals {
		for _, to := range all {
			t.Run(fmt.Sprintf("reject terminal %s->%s", term, to), func(t *testing.T) {
				if ValidRunTransition(term, to) {
					t.Errorf("ValidRunTransition(%q, %q) = true, want false", term, to)
				}
			})
		}
	}
}

// TestTypesIsResumableRunStatus asserts the resumable run status contract.
func TestTypesIsResumableRunStatus(t *testing.T) {
	resumable := []RunStatus{
		RunStatusPending,
		RunStatusRunning,
		RunStatusWaitingApproval,
	}
	for _, s := range resumable {
		if !IsResumableRunStatus(s) {
			t.Errorf("IsResumableRunStatus(%q) = false, want true", s)
		}
	}
	notResumable := []RunStatus{
		RunStatusDeliveryPending,
		RunStatusSucceeded,
		RunStatusFailed,
		RunStatusCanceled,
		RunStatusTimedOut,
		RunStatusDeliveryFailed,
	}
	for _, s := range notResumable {
		if IsResumableRunStatus(s) {
			t.Errorf("IsResumableRunStatus(%q) = true, want false", s)
		}
	}
}

// TestIsTerminalRunStatus asserts the terminal run status contract.
func TestIsTerminalRunStatus(t *testing.T) {
	terminal := []RunStatus{
		RunStatusSucceeded,
		RunStatusFailed,
		RunStatusCanceled,
		RunStatusTimedOut,
		RunStatusDeliveryFailed,
	}
	for _, s := range terminal {
		if !IsTerminalRunStatus(s) {
			t.Errorf("IsTerminalRunStatus(%q) = false, want true", s)
		}
	}
	active := []RunStatus{
		RunStatusPending,
		RunStatusRunning,
		RunStatusWaitingApproval,
		RunStatusDeliveryPending,
	}
	for _, s := range active {
		if IsTerminalRunStatus(s) {
			t.Errorf("IsTerminalRunStatus(%q) = true, want false", s)
		}
	}
}

// TestValidAttemptTransition asserts the attempt status transition contract.
func TestValidAttemptTransition(t *testing.T) {
	all := []AttemptStatus{
		AttemptStatusPending,
		AttemptStatusRunning,
		AttemptStatusSucceeded,
		AttemptStatusFailed,
		AttemptStatusTimedOut,
		AttemptStatusCanceled,
		AttemptStatusInterrupted,
	}
	accept := []struct{ from, to AttemptStatus }{
		{AttemptStatusPending, AttemptStatusRunning},
		{AttemptStatusRunning, AttemptStatusSucceeded},
		{AttemptStatusRunning, AttemptStatusFailed},
		{AttemptStatusRunning, AttemptStatusTimedOut},
		{AttemptStatusRunning, AttemptStatusCanceled},
		{AttemptStatusRunning, AttemptStatusInterrupted},
	}
	for _, tc := range accept {
		t.Run(fmt.Sprintf("accept %s->%s", tc.from, tc.to), func(t *testing.T) {
			if !ValidAttemptTransition(tc.from, tc.to) {
				t.Errorf("ValidAttemptTransition(%q, %q) = false, want true", tc.from, tc.to)
			}
		})
	}
	reject := []struct{ from, to AttemptStatus }{
		{AttemptStatusPending, AttemptStatusSucceeded},
		{AttemptStatusRunning, AttemptStatusRunning},
		{AttemptStatusRunning, AttemptStatusPending},
	}
	for _, tc := range reject {
		t.Run(fmt.Sprintf("reject %s->%s", tc.from, tc.to), func(t *testing.T) {
			if ValidAttemptTransition(tc.from, tc.to) {
				t.Errorf("ValidAttemptTransition(%q, %q) = true, want false", tc.from, tc.to)
			}
		})
	}
	// Terminal statuses reject every destination, including reflexive edges.
	terminals := []AttemptStatus{
		AttemptStatusSucceeded,
		AttemptStatusFailed,
		AttemptStatusTimedOut,
		AttemptStatusCanceled,
		AttemptStatusInterrupted,
	}
	for _, term := range terminals {
		for _, to := range all {
			t.Run(fmt.Sprintf("reject terminal %s->%s", term, to), func(t *testing.T) {
				if ValidAttemptTransition(term, to) {
					t.Errorf("ValidAttemptTransition(%q, %q) = true, want false", term, to)
				}
			})
		}
	}
}

// TestIsTerminalAttemptStatus asserts the terminal attempt status contract.
func TestIsTerminalAttemptStatus(t *testing.T) {
	terminal := []AttemptStatus{
		AttemptStatusSucceeded,
		AttemptStatusFailed,
		AttemptStatusTimedOut,
		AttemptStatusCanceled,
		AttemptStatusInterrupted,
	}
	for _, s := range terminal {
		if !IsTerminalAttemptStatus(s) {
			t.Errorf("IsTerminalAttemptStatus(%q) = false, want true", s)
		}
	}
	active := []AttemptStatus{
		AttemptStatusPending,
		AttemptStatusRunning,
	}
	for _, s := range active {
		if IsTerminalAttemptStatus(s) {
			t.Errorf("IsTerminalAttemptStatus(%q) = true, want false", s)
		}
	}
}

// TestIsTerminalStepID asserts the reserved terminal step ID contract.
func TestIsTerminalStepID(t *testing.T) {
	for _, id := range []string{"success", "failure"} {
		if !IsTerminalStepID(id) {
			t.Errorf("IsTerminalStepID(%q) = false, want true", id)
		}
	}
	for _, id := range []string{"plan", "implement", "", "successful"} {
		if IsTerminalStepID(id) {
			t.Errorf("IsTerminalStepID(%q) = true, want false", id)
		}
	}
}

// TestCloneRunSnapshot asserts RunSnapshot.Clone makes a deep copy.
func TestCloneRunSnapshot(t *testing.T) {
	deadline := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	finished := time.Date(2025, 6, 1, 13, 0, 0, 0, time.UTC)
	orig := RunSnapshot{
		RunID:          "run_1",
		WorkflowName:   "checkout",
		WorkflowDigest: "wf-digest",
		SnapshotDigest: "snap-digest",
		InputDigest:    "input-digest",
		Status:         RunStatusRunning,
		ActiveStepID:   "implement",
		BaseRef:        "refs/heads/main",
		BaseCommit:     "abc123",
		WorktreeName:   "wt-1",
		Version:        7,
		StartedAt:      time.Date(2025, 6, 1, 11, 0, 0, 0, time.UTC),
		DeadlineAt:     &deadline,
		FinishedAt:     &finished,
	}

	clone := orig.Clone()

	if clone.DeadlineAt == nil || clone.FinishedAt == nil {
		t.Errorf("Clone() left non-nil pointers nil: deadline=%v finished=%v", clone.DeadlineAt, clone.FinishedAt)
	}
	if clone.DeadlineAt == orig.DeadlineAt || clone.FinishedAt == orig.FinishedAt {
		t.Errorf("Clone() shares pointer targets with original")
	}
	if clone.DeadlineAt != nil && *clone.DeadlineAt != deadline {
		t.Errorf("Clone() deadline = %v, want %v", *clone.DeadlineAt, deadline)
	}
	if clone.FinishedAt != nil && *clone.FinishedAt != finished {
		t.Errorf("Clone() finished = %v, want %v", *clone.FinishedAt, finished)
	}
	if clone.RunID != orig.RunID || clone.WorkflowName != orig.WorkflowName ||
		clone.WorkflowDigest != orig.WorkflowDigest || clone.SnapshotDigest != orig.SnapshotDigest ||
		clone.InputDigest != orig.InputDigest || clone.Status != orig.Status ||
		clone.ActiveStepID != orig.ActiveStepID || clone.BaseRef != orig.BaseRef ||
		clone.BaseCommit != orig.BaseCommit || clone.WorktreeName != orig.WorktreeName ||
		clone.Version != orig.Version || !clone.StartedAt.Equal(orig.StartedAt) {
		t.Errorf("Clone() = %+v, want equal scalar fields to %+v", clone, orig)
	}

	// Mutating the clone must not change the original.
	if clone.DeadlineAt != nil {
		*clone.DeadlineAt = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	if clone.FinishedAt != nil {
		*clone.FinishedAt = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	if *orig.DeadlineAt != deadline {
		t.Errorf("mutating clone.DeadlineAt changed the original: %v", *orig.DeadlineAt)
	}
	if *orig.FinishedAt != finished {
		t.Errorf("mutating clone.FinishedAt changed the original: %v", *orig.FinishedAt)
	}

	// Nil pointers must stay nil.
	nilOrig := RunSnapshot{RunID: "run_2", Status: RunStatusPending}
	nilClone := nilOrig.Clone()
	if nilClone.DeadlineAt != nil || nilClone.FinishedAt != nil {
		t.Errorf("Clone() of snapshot with nil pointers = %+v, want nil pointers", nilClone)
	}
}

// TestCloneStepAttempt asserts StepAttempt.Clone makes a deep copy.
func TestCloneStepAttempt(t *testing.T) {
	finished := time.Date(2025, 6, 1, 13, 0, 0, 0, time.UTC)
	decision := []byte(`{"route":"success","reason":"tests green"}`)
	orig := StepAttempt{
		AttemptID:        "att_1",
		RunID:            "run_1",
		StepID:           "implement",
		AttemptNo:        3,
		Status:           AttemptStatusRunning,
		CoordinatorRunID: "coord_1",
		TaskID:           "task_1",
		OutputRef:        "refs/heads/wt-1",
		OutputDigest:     "out-digest",
		ToStepID:         "success",
		TransitionIndex:  2,
		MatchDigest:      "match-digest",
		DecisionJSON:     decision,
		StartedAt:        time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC),
		FinishedAt:       &finished,
		Version:          5,
	}

	clone := orig.Clone()

	if clone.FinishedAt == nil || clone.FinishedAt == orig.FinishedAt {
		t.Errorf("Clone() must deep-copy FinishedAt: got %v", clone.FinishedAt)
	}
	if len(clone.DecisionJSON) != len(orig.DecisionJSON) {
		t.Errorf("Clone() DecisionJSON length = %d, want %d", len(clone.DecisionJSON), len(orig.DecisionJSON))
	}
	if len(clone.DecisionJSON) > 0 && &clone.DecisionJSON[0] == &orig.DecisionJSON[0] {
		t.Errorf("Clone() shares the DecisionJSON backing array with the original")
	}
	if !reflect.DeepEqual(clone.DecisionJSON, orig.DecisionJSON) {
		t.Errorf("Clone() DecisionJSON = %q, want %q", clone.DecisionJSON, orig.DecisionJSON)
	}
	if clone.AttemptID != orig.AttemptID || clone.RunID != orig.RunID ||
		clone.StepID != orig.StepID || clone.AttemptNo != orig.AttemptNo ||
		clone.Status != orig.Status || clone.CoordinatorRunID != orig.CoordinatorRunID ||
		clone.TaskID != orig.TaskID || clone.OutputRef != orig.OutputRef ||
		clone.OutputDigest != orig.OutputDigest || clone.ToStepID != orig.ToStepID ||
		clone.TransitionIndex != orig.TransitionIndex || clone.MatchDigest != orig.MatchDigest ||
		clone.Version != orig.Version || !clone.StartedAt.Equal(orig.StartedAt) {
		t.Errorf("Clone() = %+v, want equal scalar fields to %+v", clone, orig)
	}

	// Mutating the clone must not change the original.
	if len(clone.DecisionJSON) > 0 {
		clone.DecisionJSON[0] = 'X'
	}
	if clone.FinishedAt != nil {
		*clone.FinishedAt = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	if orig.DecisionJSON[0] != '{' {
		t.Errorf("mutating clone.DecisionJSON changed the original: %q", orig.DecisionJSON)
	}
	if *orig.FinishedAt != finished {
		t.Errorf("mutating clone.FinishedAt changed the original: %v", *orig.FinishedAt)
	}

	// Nil slice and pointer must stay nil.
	nilOrig := StepAttempt{AttemptID: "att_2", Status: AttemptStatusPending}
	nilClone := nilOrig.Clone()
	if nilClone.DecisionJSON != nil || nilClone.FinishedAt != nil {
		t.Errorf("Clone() of attempt with nil fields = %+v, want nil DecisionJSON/FinishedAt", nilClone)
	}
}

// TestCloneTransitionRecord asserts TransitionRecord.Clone makes a deep copy.
func TestCloneTransitionRecord(t *testing.T) {
	decision := []byte(`{"to_step":"success","index":1}`)
	orig := TransitionRecord{
		RunID:           "run_1",
		FromAttemptID:   "att_1",
		ToStepID:        "success",
		TransitionIndex: 1,
		MatchDigest:     "match-digest",
		DecisionJSON:    decision,
		CreatedAt:       time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC),
	}

	clone := orig.Clone()

	if len(clone.DecisionJSON) != len(orig.DecisionJSON) {
		t.Errorf("Clone() DecisionJSON length = %d, want %d", len(clone.DecisionJSON), len(orig.DecisionJSON))
	}
	if len(clone.DecisionJSON) > 0 && &clone.DecisionJSON[0] == &orig.DecisionJSON[0] {
		t.Errorf("Clone() shares the DecisionJSON backing array with the original")
	}
	if !reflect.DeepEqual(clone.DecisionJSON, orig.DecisionJSON) {
		t.Errorf("Clone() DecisionJSON = %q, want %q", clone.DecisionJSON, orig.DecisionJSON)
	}
	if clone.RunID != orig.RunID || clone.FromAttemptID != orig.FromAttemptID ||
		clone.ToStepID != orig.ToStepID || clone.TransitionIndex != orig.TransitionIndex ||
		clone.MatchDigest != orig.MatchDigest || !clone.CreatedAt.Equal(orig.CreatedAt) {
		t.Errorf("Clone() = %+v, want equal scalar fields to %+v", clone, orig)
	}

	// Mutating the clone must not change the original.
	if len(clone.DecisionJSON) > 0 {
		clone.DecisionJSON[0] = 'X'
	}
	if orig.DecisionJSON[0] != '{' {
		t.Errorf("mutating clone.DecisionJSON changed the original: %q", orig.DecisionJSON)
	}

	// Nil slice must stay nil.
	nilClone := (TransitionRecord{RunID: "run_2"}).Clone()
	if nilClone.DecisionJSON != nil {
		t.Errorf("Clone() of record with nil DecisionJSON = %+v, want nil", nilClone)
	}
}

// TestCloneApprovalRecord asserts ApprovalRecord.Clone makes a deep copy.
func TestCloneApprovalRecord(t *testing.T) {
	resolved := time.Date(2025, 6, 1, 14, 0, 0, 0, time.UTC)
	evidence := []byte(`{"prompt":"approve the change?"}`)
	orig := ApprovalRecord{
		ApprovalID:   "appr_1",
		RunID:        "run_1",
		StepID:       "waiting_approval",
		Status:       "pending",
		Actor:        "human",
		Reason:       "manual review",
		EvidenceJSON: evidence,
		CreatedAt:    time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC),
		ResolvedAt:   &resolved,
	}

	clone := orig.Clone()

	if clone.ResolvedAt == nil || clone.ResolvedAt == orig.ResolvedAt {
		t.Errorf("Clone() must deep-copy ResolvedAt: got %v", clone.ResolvedAt)
	}
	if clone.ResolvedAt != nil && *clone.ResolvedAt != resolved {
		t.Errorf("Clone() ResolvedAt = %v, want %v", *clone.ResolvedAt, resolved)
	}
	if len(clone.EvidenceJSON) != len(orig.EvidenceJSON) {
		t.Errorf("Clone() EvidenceJSON length = %d, want %d", len(clone.EvidenceJSON), len(orig.EvidenceJSON))
	}
	if len(clone.EvidenceJSON) > 0 && &clone.EvidenceJSON[0] == &orig.EvidenceJSON[0] {
		t.Errorf("Clone() shares the EvidenceJSON backing array with the original")
	}
	if !reflect.DeepEqual(clone.EvidenceJSON, orig.EvidenceJSON) {
		t.Errorf("Clone() EvidenceJSON = %q, want %q", clone.EvidenceJSON, orig.EvidenceJSON)
	}
	if clone.ApprovalID != orig.ApprovalID || clone.RunID != orig.RunID ||
		clone.StepID != orig.StepID || clone.Status != orig.Status ||
		clone.Actor != orig.Actor || clone.Reason != orig.Reason ||
		!clone.CreatedAt.Equal(orig.CreatedAt) {
		t.Errorf("Clone() = %+v, want equal scalar fields to %+v", clone, orig)
	}

	// Mutating the clone must not change the original.
	if len(clone.EvidenceJSON) > 0 {
		clone.EvidenceJSON[0] = 'X'
	}
	if clone.ResolvedAt != nil {
		*clone.ResolvedAt = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	if orig.EvidenceJSON[0] != '{' {
		t.Errorf("mutating clone.EvidenceJSON changed the original: %q", orig.EvidenceJSON)
	}
	if *orig.ResolvedAt != resolved {
		t.Errorf("mutating clone.ResolvedAt changed the original: %v", *orig.ResolvedAt)
	}

	// Nil slice and pointer must stay nil.
	nilClone := (ApprovalRecord{ApprovalID: "appr_2"}).Clone()
	if nilClone.EvidenceJSON != nil || nilClone.ResolvedAt != nil {
		t.Errorf("Clone() of record with nil fields = %+v, want nil EvidenceJSON/ResolvedAt", nilClone)
	}
}

// TestCloneDeliveryRecord asserts DeliveryRecord.Clone copies all values.
func TestCloneDeliveryRecord(t *testing.T) {
	orig := DeliveryRecord{
		RunID:          "run_1",
		IdempotencyKey: "idem-1",
		Mode:           "push",
		BaseRef:        "refs/heads/main",
		HeadRef:        "refs/heads/wt-1",
		CommitSHA:      "abc123",
		Provider:       "github",
		RemoteID:       "remote-1",
		URL:            "https://example.com/pull/1",
		Status:         "published",
		ErrorRef:       "err-1",
		UpdatedAt:      time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC),
	}

	clone := orig.Clone()

	if !reflect.DeepEqual(clone, orig) {
		t.Errorf("Clone() = %+v, want equal value copy of %+v", clone, orig)
	}

	// Mutating the clone must not change the original.
	clone.Status = "failed"
	clone.UpdatedAt = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	if orig.Status != "published" || !orig.UpdatedAt.Equal(time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("mutating clone changed the original: %+v", orig)
	}
}

// TestSnapshotJSONRunSnapshot asserts the RunSnapshot JSON round trip.
func TestSnapshotJSONRunSnapshot(t *testing.T) {
	deadline := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	finished := time.Date(2025, 6, 1, 13, 0, 0, 0, time.UTC)
	orig := RunSnapshot{
		RunID:            "run_1",
		WorkflowName:     "checkout",
		WorkflowDigest:   "wf-digest",
		SnapshotDigest:   "snap-digest",
		InputDigest:      "input-digest",
		Status:           RunStatusSucceeded,
		ActiveStepID:     "success",
		BaseRef:          "refs/heads/main",
		BaseCommit:       "abc123",
		OriginBaseCommit: "origin-abc123",
		WorktreeName:     "wt-1",
		Version:          9,
		StartedAt:        time.Date(2025, 6, 1, 11, 0, 0, 0, time.UTC),
		DeadlineAt:       &deadline,
		FinishedAt:       &finished,
	}

	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal(RunSnapshot) failed: %v", err)
	}
	var got RunSnapshot
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal(RunSnapshot) failed: %v", err)
	}
	if !reflect.DeepEqual(orig, got) {
		t.Errorf("RunSnapshot JSON round trip mismatch:\n  orig: %+v\n  got:  %+v\n  json: %s", orig, got, data)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(data, &keys); err != nil {
		t.Fatalf("Unmarshal(RunSnapshot) as map failed: %v", err)
	}
	for _, want := range []string{
		"run_id", "workflow_name", "workflow_digest", "snapshot_digest",
		"input_digest", "status", "active_step_id", "base_ref", "base_commit",
		"origin_base_commit", "worktree_name", "version", "started_at", "deadline_at", "finished_at",
	} {
		if _, ok := keys[want]; !ok {
			t.Errorf("RunSnapshot JSON missing key %q: %s", want, data)
		}
	}
}

// TestSnapshotJSONStepAttempt asserts the StepAttempt JSON round trip.
func TestSnapshotJSONStepAttempt(t *testing.T) {
	finished := time.Date(2025, 6, 1, 13, 0, 0, 0, time.UTC)
	orig := StepAttempt{
		AttemptID:        "att_1",
		RunID:            "run_1",
		StepID:           "implement",
		AttemptNo:        3,
		Status:           AttemptStatusSucceeded,
		CoordinatorRunID: "coord_1",
		TaskID:           "task_1",
		OutputRef:        "refs/heads/wt-1",
		OutputDigest:     "out-digest",
		ToStepID:         "success",
		TransitionIndex:  2,
		MatchDigest:      "match-digest",
		DecisionJSON:     []byte(`{"route":"success"}`),
		StartedAt:        time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC),
		FinishedAt:       &finished,
		Version:          5,
	}

	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal(StepAttempt) failed: %v", err)
	}
	var got StepAttempt
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal(StepAttempt) failed: %v", err)
	}
	if !reflect.DeepEqual(orig, got) {
		t.Errorf("StepAttempt JSON round trip mismatch:\n  orig: %+v\n  got:  %+v\n  json: %s", orig, got, data)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(data, &keys); err != nil {
		t.Fatalf("Unmarshal(StepAttempt) as map failed: %v", err)
	}
	for _, want := range []string{
		"attempt_id", "run_id", "step_id", "attempt_no", "status",
		"coordinator_run_id", "task_id", "output_ref", "output_digest",
		"to_step_id", "transition_index", "match_digest", "decision_json",
		"started_at", "finished_at", "version",
	} {
		if _, ok := keys[want]; !ok {
			t.Errorf("StepAttempt JSON missing key %q: %s", want, data)
		}
	}
}
