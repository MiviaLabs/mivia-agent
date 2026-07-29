package cli

import (
	"context"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
)

func TestDiagnostics_ListRuns(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	ctx := context.Background()

	// Create some runs
	for i := 0; i < 3; i++ {
		runID := string(rune('a' + i))
		snap := ledger.RunSnapshot{
			RunID:       runID,
			DisplayName: "run-" + runID,
			Status:      ledger.RunStatusCompleted,
			CreatedAt:   time.Now().Add(-time.Duration(i) * time.Hour),
		}
		if err := repo.CreateRun(ctx, "", snap); err != nil {
			t.Fatal(err)
		}
	}

	diag := NewDiagnostics(repo, nil)
	runs, err := diag.ListRuns(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 3 {
		t.Fatalf("expected 3 runs, got %d", len(runs))
	}
	if runs[0].RunID == "" {
		t.Fatal("expected non-empty RunID in summary")
	}
}

func TestDiagnostics_ActiveHandles(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	ctx := context.Background()

	// Create one completed run and one running run
	_ = repo.CreateRun(ctx, "", ledger.RunSnapshot{RunID: "r1", Status: ledger.RunStatusCompleted})
	_ = repo.CreateRun(ctx, "", ledger.RunSnapshot{RunID: "r2", Status: ledger.RunStatusRunning})

	diag := NewDiagnostics(repo, nil)
	count := diag.ActiveHandles()
	if count != 1 {
		t.Fatalf("expected 1 active handle (r2 running), got %d", count)
	}
}

func TestDiagnostics_ActiveHandlesFiltersCorrectly(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	ctx := context.Background()

	_ = repo.CreateRun(ctx, "", ledger.RunSnapshot{RunID: "r1", Status: ledger.RunStatusCreated})
	_ = repo.CreateRun(ctx, "", ledger.RunSnapshot{RunID: "r2", Status: ledger.RunStatusQueued})
	_ = repo.CreateRun(ctx, "", ledger.RunSnapshot{RunID: "r3", Status: ledger.RunStatusRunning})
	_ = repo.CreateRun(ctx, "", ledger.RunSnapshot{RunID: "r4", Status: ledger.RunStatusCompleted})
	_ = repo.CreateRun(ctx, "", ledger.RunSnapshot{RunID: "r5", Status: ledger.RunStatusFailed})
	_ = repo.CreateRun(ctx, "", ledger.RunSnapshot{RunID: "r6", Status: ledger.RunStatusCanceled})

	diag := NewDiagnostics(repo, nil)
	count := diag.ActiveHandles()
	if count != 3 { // created + queued + running = 3
		t.Fatalf("expected 3 active handles (created+queued+running), got %d", count)
	}
}

func TestDiagnostics_MetricsSnapshot(t *testing.T) {
	adapter := events.NewMetricsAdapter()
	bus := events.New()
	adapter.Subscribe(bus)

	bus.Publish(events.NewEvent(events.KindToolStart))
	bus.Publish(events.NewEvent(events.KindToolEnd))

	diag := NewDiagnostics(nil, adapter)
	counts, total := diag.MetricsSnapshot()
	if total != 2 {
		t.Fatalf("expected total=2, got %d", total)
	}
	if len(counts) != 2 {
		t.Fatalf("expected 2 kind entries, got %d", len(counts))
	}
}

func TestDiagnostics_NilRepo(t *testing.T) {
	diag := NewDiagnostics(nil, nil)
	ctx := context.Background()

	// Should not panic
	runs, err := diag.ListRuns(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("expected empty runs from nil repo, got %d", len(runs))
	}
	if diag.ActiveHandles() != 0 {
		t.Fatalf("expected 0 active handles from nil repo")
	}
	counts, total := diag.MetricsSnapshot()
	if len(counts) != 0 || total != 0 {
		t.Fatalf("expected zero metrics from nil adapter")
	}
}

func TestDiagnostics_ListRunsLimit(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		snap := ledger.RunSnapshot{
			RunID:     string(rune('a' + i)),
			Status:    ledger.RunStatusCompleted,
			CreatedAt: time.Now(),
		}
		_ = repo.CreateRun(ctx, "", snap)
	}

	diag := NewDiagnostics(repo, nil)
	runs, err := diag.ListRuns(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) > 2 {
		t.Fatalf("expected at most 2 runs with limit=2, got %d", len(runs))
	}
}
