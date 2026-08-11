package localengine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// TestWriteRunSummaryCreatesRunsDirAndFile pins the durability fix: the
// harness creates .mivia/runs on demand and writes a per-run JSON summary, so
// a workspace without the directory still gets a durable local record.
func TestWriteRunSummaryCreatesRunsDirAndFile(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	ctx := context.Background()
	root := t.TempDir()
	const runID = "wfr-sum"

	snapshot, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{
		SchemaVersion: 1, DefinitionTOML: []byte("name=x"), DefinitionDigest: "digest",
		Inputs: map[string]string{"task": "build"},
	})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	if err := repo.CreateRun(ctx, workflowledger.RunSnapshot{
		RunID: runID, WorkflowName: "feature-delivery", WorkflowDigest: "digest",
		SnapshotDigest: workflowledger.SnapshotDigest(snapshot),
		InputDigest:    workflowledger.InputDigest(map[string]string{"task": "build"}),
		Status:         workflowledger.RunStatusPending, ActiveStepID: "plan",
		StartedAt: started, BaseRef: "master", BaseCommit: "abc123", WorktreeName: "wf-" + runID,
	}, snapshot); err != nil {
		t.Fatal(err)
	}

	if err := writeRunSummary(ctx, repo, root, runID); err != nil {
		t.Fatalf("writeRunSummary: %v", err)
	}

	path := filepath.Join(root, ".mivia", "runs", runID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read summary %s: %v", path, err)
	}
	var got runSummaryFile
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal summary: %v", err)
	}
	if got.RunID != runID || got.Workflow != "feature-delivery" || got.Status != "pending" {
		t.Fatalf("summary = %+v, want run %s feature-delivery pending", got, runID)
	}
	if got.BaseRef != "master" || got.BaseCommit != "abc123" || got.Worktree != "wf-"+runID {
		t.Fatalf("summary identity fields missing: %+v", got)
	}
	if got.StartedAt != started.UTC().Format(time.RFC3339) {
		t.Fatalf("started_at = %q, want %q", got.StartedAt, started.UTC().Format(time.RFC3339))
	}
}

// TestWriteRunSummarySurfacesDeliveryError pins that a failed delivery's
// stored failure text lands in the on-disk summary (the automatic hint).
func TestWriteRunSummarySurfacesDeliveryError(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	ctx := context.Background()
	root := t.TempDir()
	const runID = "wfr-sum-del"

	snapshot, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{
		SchemaVersion: 1, DefinitionTOML: []byte("name=x"), DefinitionDigest: "digest",
		Inputs: map[string]string{"task": "build"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateRun(ctx, workflowledger.RunSnapshot{
		RunID: runID, WorkflowName: "feature-delivery", WorkflowDigest: "digest",
		SnapshotDigest: workflowledger.SnapshotDigest(snapshot),
		InputDigest:    workflowledger.InputDigest(map[string]string{"task": "build"}),
		Status:         workflowledger.RunStatusPending,
		StartedAt:      time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC),
	}, snapshot); err != nil {
		t.Fatal(err)
	}
	// CreateRun starts a run in pending; delivery_pending needs legal
	// transitions (pending -> running -> delivery_pending).
	for _, next := range []workflowledger.RunStatus{
		workflowledger.RunStatusRunning,
		workflowledger.RunStatusDeliveryPending,
	} {
		run, getErr := repo.GetRun(ctx, runID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		if casErr := repo.CompareAndSetRunStatus(ctx, runID, run.Version, next, nil); casErr != nil {
			t.Fatal(casErr)
		}
	}
	errText := "git push origin HEAD:refs/heads/wf/x: signal: killed: pre-push hook"
	ref := "sha256:" + workflowledger.DigestHex([]byte(errText))
	if err := repo.StoreContent(ctx, ref, []byte(errText)); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertDelivery(ctx, workflowledger.DeliveryRecord{
		RunID: runID, IdempotencyKey: "wfdel:k", Mode: "draft", BaseRef: "master",
		HeadRef: "wf/x", Provider: "github", Status: "failed", ErrorRef: ref,
	}); err != nil {
		t.Fatal(err)
	}

	if err := writeRunSummary(ctx, repo, root, runID); err != nil {
		t.Fatalf("writeRunSummary: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, ".mivia", "runs", runID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var got runSummaryFile
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Deliveries) != 1 || got.Deliveries[0].Status != "failed" {
		t.Fatalf("deliveries = %+v, want one failed", got.Deliveries)
	}
	if got.Deliveries[0].ErrorText != errText {
		t.Fatalf("error text = %q, want %q", got.Deliveries[0].ErrorText, errText)
	}
}
