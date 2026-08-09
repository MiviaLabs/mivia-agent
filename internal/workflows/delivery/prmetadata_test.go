package delivery

import (
	"context"
	"fmt"
	"testing"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// newPRMetadataRepo builds a memory ledger with one delivery_pending run.
func newPRMetadataRepo(t *testing.T) (workflowledger.Repository, workflowledger.RunSnapshot) {
	t.Helper()
	repo := workflowledger.NewMemoryRepository()
	ctx := context.Background()
	run := workflowledger.RunSnapshot{
		RunID:          "wfr-prmeta",
		WorkflowName:   "wf",
		WorkflowDigest: "digest",
		Status:         workflowledger.RunStatusPending,
		ActiveStepID:   "implement",
		WorktreeName:   "wt",
		RemoteURL:      "https://github.com/o/r",
	}
	if err := repo.CreateRun(ctx, run, []byte("snapshot")); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	cur, err := repo.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	for _, next := range []workflowledger.RunStatus{workflowledger.RunStatusRunning, workflowledger.RunStatusDeliveryPending} {
		if err := repo.CompareAndSetRunStatus(ctx, run.RunID, cur.Version, next, nil); err != nil {
			t.Fatalf("CAS to %s: %v", next, err)
		}
		cur, err = repo.GetRun(ctx, run.RunID)
		if err != nil {
			t.Fatalf("GetRun: %v", err)
		}
	}
	return repo, cur
}

// seedAttempt records one completed step attempt in event order. outputJSON
// and errorText become the attempt's stored output and error content when
// non-empty.
func seedAttempt(t *testing.T, repo workflowledger.Repository, runID, stepID string, attemptNo int, outputJSON, errorText string) {
	t.Helper()
	ctx := context.Background()
	attemptID := fmt.Sprintf("a-%s-%d", stepID, attemptNo)
	attempt := workflowledger.StepAttempt{
		RunID: runID, StepID: stepID, AttemptNo: attemptNo, AttemptID: attemptID,
	}
	if err := repo.CreateStepAttempt(ctx, attempt); err != nil {
		t.Fatalf("CreateStepAttempt(%s, %d): %v", stepID, attemptNo, err)
	}
	created, err := repo.GetStepAttempt(ctx, runID, attemptID)
	if err != nil {
		t.Fatalf("GetStepAttempt: %v", err)
	}
	outcome := workflowledger.AttemptOutcome{Status: workflowledger.AttemptStatusSucceeded}
	if outputJSON != "" {
		ref := "sha256:" + workflowledger.DigestHex([]byte(outputJSON))
		if err := repo.StoreContent(ctx, ref, []byte(outputJSON)); err != nil {
			t.Fatalf("StoreContent: %v", err)
		}
		outcome.OutputRef = ref
	}
	if errorText != "" {
		ref := "sha256:" + workflowledger.DigestHex([]byte(errorText))
		if err := repo.StoreContent(ctx, ref, []byte(errorText)); err != nil {
			t.Fatalf("StoreContent: %v", err)
		}
		outcome.ErrorRef = ref
		outcome.Status = workflowledger.AttemptStatusFailed
	}
	if err := repo.CompleteStepAttempt(ctx, runID, attemptID, created.Version, outcome); err != nil {
		t.Fatalf("CompleteStepAttempt(%s, %d): %v", stepID, attemptNo, err)
	}
}

func TestLatestFailureText(t *testing.T) {
	ctx := context.Background()

	t.Run("no attempts returns empty", func(t *testing.T) {
		repo, run := newPRMetadataRepo(t)
		got, err := LatestFailureText(ctx, repo, run.RunID)
		if err != nil {
			t.Fatalf("LatestFailureText: %v", err)
		}
		if got != "" {
			t.Fatalf("LatestFailureText = %q, want empty", got)
		}
	})

	t.Run("repair attempt with error ref returns text", func(t *testing.T) {
		repo, run := newPRMetadataRepo(t)
		seedAttempt(t, repo, run.RunID, DeliveryRepairStepID, 1, "", "commit hook rejected the change")
		got, err := LatestFailureText(ctx, repo, run.RunID)
		if err != nil {
			t.Fatalf("LatestFailureText: %v", err)
		}
		if got != "commit hook rejected the change" {
			t.Fatalf("LatestFailureText = %q, want the stored failure text", got)
		}
	})

	t.Run("non-repair attempts ignored", func(t *testing.T) {
		repo, run := newPRMetadataRepo(t)
		seedAttempt(t, repo, run.RunID, "implement", 1, "", "some other failure")
		got, err := LatestFailureText(ctx, repo, run.RunID)
		if err != nil {
			t.Fatalf("LatestFailureText: %v", err)
		}
		if got != "" {
			t.Fatalf("LatestFailureText = %q, want empty", got)
		}
	})

	t.Run("highest attempt number wins", func(t *testing.T) {
		repo, run := newPRMetadataRepo(t)
		seedAttempt(t, repo, run.RunID, DeliveryRepairStepID, 1, "", "first")
		seedAttempt(t, repo, run.RunID, DeliveryRepairStepID, 2, "", "second")
		got, err := LatestFailureText(ctx, repo, run.RunID)
		if err != nil {
			t.Fatalf("LatestFailureText: %v", err)
		}
		if got != "second" {
			t.Fatalf("LatestFailureText = %q, want the highest attempt's text", got)
		}
	})

	t.Run("empty error ref skipped", func(t *testing.T) {
		repo, run := newPRMetadataRepo(t)
		seedAttempt(t, repo, run.RunID, DeliveryRepairStepID, 1, `{"pr_title": "feat: x"}`, "")
		got, err := LatestFailureText(ctx, repo, run.RunID)
		if err != nil {
			t.Fatalf("LatestFailureText: %v", err)
		}
		if got != "" {
			t.Fatalf("LatestFailureText = %q, want empty for an attempt without an error ref", got)
		}
	})
}

// resolveLatestChangeSummary resolves the change summary for the run and
// fails the test when resolution errors.
func resolveLatestChangeSummary(t *testing.T, repo workflowledger.Repository, runID string) map[string]any {
	t.Helper()
	got, err := ResolveLatestChangeSummary(context.Background(), repo, runID)
	if err != nil {
		t.Fatalf("ResolveLatestChangeSummary: %v", err)
	}
	return got
}

// assertResolvedNil asserts the resolved change summary is nil.
func assertResolvedNil(t *testing.T, repo workflowledger.Repository, runID string) {
	t.Helper()
	if got := resolveLatestChangeSummary(t, repo, runID); got != nil {
		t.Fatalf("ResolveLatestChangeSummary = %v, want nil", got)
	}
}

// assertResolvedChangeSummary asserts the resolved summary holds every
// expected value.
func assertResolvedChangeSummary(t *testing.T, repo workflowledger.Repository, runID string, want map[string]any) {
	t.Helper()
	got := resolveLatestChangeSummary(t, repo, runID)
	for key, wantVal := range want {
		if got == nil || got[key] != wantVal {
			t.Fatalf("ResolveLatestChangeSummary = %v, want %s=%v", got, key, wantVal)
		}
	}
}

func TestResolveLatestChangeSummary(t *testing.T) {
	t.Run("no outputs returns nil", func(t *testing.T) {
		repo, run := newPRMetadataRepo(t)
		assertResolvedNil(t, repo, run.RunID)
	})

	t.Run("implement output with pr_title returned", func(t *testing.T) {
		repo, run := newPRMetadataRepo(t)
		seedAttempt(t, repo, run.RunID, "implement", 1, `{"pr_title": "feat: x", "pr_summary": "S."}`, "")
		assertResolvedChangeSummary(t, repo, run.RunID, map[string]any{"pr_title": "feat: x", "pr_summary": "S."})
	})

	t.Run("repair output with newer pr_title wins", func(t *testing.T) {
		repo, run := newPRMetadataRepo(t)
		seedAttempt(t, repo, run.RunID, "implement", 1, `{"pr_title": "old title"}`, "")
		seedAttempt(t, repo, run.RunID, DeliveryRepairStepID, 2, `{"pr_title": "new title"}`, "")
		assertResolvedChangeSummary(t, repo, run.RunID, map[string]any{"pr_title": "new title"})
	})

	t.Run("output without pr_title ignored", func(t *testing.T) {
		repo, run := newPRMetadataRepo(t)
		seedAttempt(t, repo, run.RunID, "implement", 1, `{"verdict": "approved"}`, "")
		assertResolvedNil(t, repo, run.RunID)
	})

	t.Run("empty pr_title ignored", func(t *testing.T) {
		repo, run := newPRMetadataRepo(t)
		seedAttempt(t, repo, run.RunID, "implement", 1, `{"pr_title": ""}`, "")
		assertResolvedNil(t, repo, run.RunID)
	})

	t.Run("invalid json output skipped without error", func(t *testing.T) {
		repo, run := newPRMetadataRepo(t)
		seedAttempt(t, repo, run.RunID, "implement", 1, "not json", "")
		assertResolvedNil(t, repo, run.RunID)
	})

	t.Run("later attempt with same number wins", func(t *testing.T) {
		repo, run := newPRMetadataRepo(t)
		seedAttempt(t, repo, run.RunID, "implement", 1, `{"pr_title": "first"}`, "")
		seedAttempt(t, repo, run.RunID, "verify", 1, `{"pr_title": "second"}`, "")
		assertResolvedChangeSummary(t, repo, run.RunID, map[string]any{"pr_title": "second"})
	})
}
