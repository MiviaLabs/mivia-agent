package cliorchestrate

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
)

// RunSummary is a bounded summary of an orchestration run.
type RunSummary struct {
	RunID       string    `json:"run_id"`
	DisplayName string    `json:"display_name"`
	Status      string    `json:"status"`
	TaskCount   int       `json:"task_count"`
	CreatedAt   time.Time `json:"created_at"`
	Elapsed     string    `json:"elapsed"`
}

// Diagnostics exposes bounded, privacy-safe operator views of the
// orchestration runtime. Sources: LedgerRepository for run state.
type Diagnostics struct {
	repo ledger.LedgerRepository
}

// NewDiagnostics creates a Diagnostics backed by the given ledger repo.
// If repo is nil, ListRuns and ActiveHandles return zero values.
// Does not panic.
func NewDiagnostics(repo ledger.LedgerRepository) *Diagnostics {
	return &Diagnostics{repo: repo}
}

// ListRuns returns runs from the ledger repository, newest first.
// limit caps the response. limit <= 0 returns all.
// Returns empty slice if no repo configured.
func (d *Diagnostics) ListRuns(ctx context.Context, limit int) ([]RunSummary, error) {
	if d.repo == nil {
		return []RunSummary{}, nil
	}

	runs, err := d.repo.ListRuns(ctx)
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}

	// Sort by CreatedAt descending (newest first)
	sort.Slice(runs, func(i, j int) bool {
		return runs[i].CreatedAt.After(runs[j].CreatedAt)
	})

	if limit > 0 && len(runs) > limit {
		runs = runs[:limit]
	}

	summaries := make([]RunSummary, 0, len(runs))
	for _, r := range runs {
		elapsed := ""
		if !r.CreatedAt.IsZero() {
			dur := time.Since(r.CreatedAt).Truncate(time.Second)
			elapsed = dur.String()
		}
		summaries = append(summaries, RunSummary{
			RunID:       r.RunID,
			DisplayName: r.DisplayName,
			Status:      string(r.Status),
			TaskCount:   len(r.Tasks),
			CreatedAt:   r.CreatedAt,
			Elapsed:     elapsed,
		})
	}
	return summaries, nil
}

// ActiveHandles returns count of non-terminal runs (running + queued + created).
// Derived from LedgerRepository.ListRuns with status filter.
// Returns 0 if no repo configured.
func (d *Diagnostics) ActiveHandles() int {
	if d.repo == nil {
		return 0
	}

	// Count runs with non-terminal statuses
	runs, err := d.repo.ListRuns(context.Background(),
		ledger.RunStatusRunning,
		ledger.RunStatusQueued,
		ledger.RunStatusCreated,
	)
	if err != nil {
		return 0
	}
	return len(runs)
}
