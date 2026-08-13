package delivery

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	ledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// DeliveryRepairStepID is the synthetic step id of the cli repair path for
// delivery failures. The cli declares its own unexported constant of the same
// value; this package re-declares it so the ledger queries below never import
// the cli package.
const DeliveryRepairStepID = "wf-delivery"

// LatestFailureText returns the stored failure text of the latest
// wf-delivery repair attempt that carries an error ref, or "" when none
// exists. The attempt with the highest AttemptNo wins; a later attempt in
// event order wins a tie. Only a storage or load failure is an error.
func LatestFailureText(ctx context.Context, repo ledger.Repository, runID string) (string, error) {
	attempts, err := repo.ListStepAttempts(ctx, runID)
	if err != nil {
		return "", err
	}
	var latest ledger.StepAttempt
	found := false
	for _, attempt := range attempts {
		if attempt.StepID != DeliveryRepairStepID || attempt.ErrorRef == "" {
			continue
		}
		if !found || attempt.AttemptNo >= latest.AttemptNo {
			latest, found = attempt, true
		}
	}
	if !found {
		return "", nil
	}
	data, err := repo.LoadContent(ctx, latest.ErrorRef)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// ResolveLatestChangeSummary returns the change-summary object of the latest
// attempt whose output is a JSON object carrying a non-empty string
// "pr_title" key, or nil when none exists. "Latest" is GLOBAL event order:
// the attempt that most recently recorded a change summary wins. Per-step
// AttemptNo is NOT comparable across steps - a repair step that re-ran after
// an implement step always has a lower AttemptNo than implement's later
// attempt even though it happened after it - so the attempt with the latest
// StartedAt wins, and a later attempt in event order wins a tie (including
// all-zero synthetic fixtures). Only schema-validated outputs ever carry an
// OutputRef, so the presence of pr_title marks a change summary. Only a
// storage or load failure is an error; an output that is not valid JSON is
// skipped.
func ResolveLatestChangeSummary(ctx context.Context, repo ledger.Repository, runID string) (map[string]any, error) {
	attempts, err := repo.ListStepAttempts(ctx, runID)
	if err != nil {
		return nil, err
	}
	var latest map[string]any
	var latestAt time.Time
	found := false
	for _, attempt := range attempts {
		if attempt.OutputRef == "" {
			continue
		}
		data, err := repo.LoadContent(ctx, attempt.OutputRef)
		if err != nil {
			return nil, err
		}
		var obj map[string]any
		if err := json.Unmarshal(data, &obj); err != nil {
			continue
		}
		title, ok := obj["pr_title"].(string)
		if !ok || strings.TrimSpace(title) == "" {
			continue
		}
		// StartedAt is the global creation order; a tie (equal timestamps,
		// including all-zero fixtures) falls to the later entry in event order.
		if !found || !attempt.StartedAt.Before(latestAt) {
			latest, found = obj, true
			latestAt = attempt.StartedAt
		}
	}
	if !found {
		return nil, nil
	}
	return latest, nil
}
