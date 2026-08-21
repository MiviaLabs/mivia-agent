package legacytui

// Throwaway reproduction for the "run dialog does not show PR link" report.
// Seeds a real store with a succeeded run + delivery record and runs the
// dialog's full live data path: workflowRunDialogLoad -> buildWorkflowRunView
// -> contentLines. Remove after diagnosis.

import (
	"context"
	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"strings"
	"testing"
	"time"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

func TestReproRunDialogShowsPRLink(t *testing.T) {
	root, configPath, storePath, raw, run := newGatedApprovalWorkspace(t)
	store, err := cli.OpenContextStorePath(storePath)
	if err != nil {
		t.Fatal(err)
	}
	repo := workflowledger.NewStorageRepository(store)
	ctx := context.Background()
	if err := repo.CreateRun(ctx, run, raw); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertDelivery(ctx, workflowledger.DeliveryRecord{
		RunID: run.RunID, IdempotencyKey: "k-" + run.RunID,
		Mode: "draft", BaseRef: "main", HeadRef: "wf/" + run.RunID,
		Provider: "github", RemoteID: "158",
		URL:       "https://github.com/MiviaLabs/mivia-agent/pull/158",
		Status:    "succeeded",
		CommitSHA: "40718667a7b2d59bf751195374c622fc02ab60fb",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	data, err := workflowRunDialogLoad(root, configPath, run.RunID)
	if err != nil {
		t.Fatalf("workflowRunDialogLoad: %v", err)
	}
	t.Logf("loaded deliveries: %d", len(data.deliveries))
	for _, d := range data.deliveries {
		t.Logf("  delivery status=%q remoteID=%q url=%q", d.Status, d.RemoteID, d.URL)
	}
	view, err := buildWorkflowRunView(data.run, data.compiled, data.attempts, data.approvals, time.Now(), workflowRunDeliveryClaim{}, data.deliveries)
	if err != nil {
		t.Fatal(err)
	}
	dlg := &workflowRunDialog{runID: run.RunID, view: view}
	rendered := cli.StripANSI(strings.Join(dlg.contentLines(), "\n"))
	t.Logf("--- dialog content ---\n%s", rendered)
	if !strings.Contains(rendered, "pull/158") {
		t.Fatalf("dialog content missing the PR link:\n%s", rendered)
	}
	// Also render the framed panel like the TUI would.
	panel, _ := dlg.ViewAt(100, 30)
	plain := cli.StripANSI(panel)
	if !strings.Contains(plain, "PR #158") || !strings.Contains(plain, "github.com") {
		t.Fatalf("framed panel missing the PR link:\n%s", plain)
	}
}
