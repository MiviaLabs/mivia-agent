package cli

import (
	"context"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// TestSessionReconcileParkedDeliveries proves a delivery_pending run left
// over from an earlier session (a restart or a crash) is published when the
// harness wires its workflow surface. Delivery authorization comes from the
// workflow's [delivery] policy - no allow_publish flag and no manual override
// is involved (the session launch path carries none). The run must settle
// succeeded and exactly one PR must be created.
func TestSessionReconcileParkedDeliveries(t *testing.T) {
	root, storePath, configPath, prRecorder := newDeliveryFixture(t)
	runID := runFixtureToDeliveryPending(t, root, configPath)
	repo := openDeliveryStore(t, storePath)
	seedWorktreeChange(t, root, runID, repo)

	res, err := config.Load(config.LoadOptions{ConfigPath: configPath, AllowMissingConfig: true})
	if err != nil {
		t.Fatal(err)
	}
	applyWorkflowStoreRoot(res, root)

	var opts tools.DefaultOptions
	// A non-nil event-bus provider marks production wiring and arms the
	// parked-run sweep; the nil-provider test paths never sweep.
	wireWorkflowToolOptions(&opts, root, res, func() *events.Bus { return nil })

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		run, err := repo.GetRun(context.Background(), runID)
		if err != nil {
			t.Fatal(err)
		}
		if run.Status != workflowledger.RunStatusDeliveryPending {
			break
		}
		select {
		case <-time.After(20 * time.Millisecond):
		}
	}
	run, err := repo.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run status = %q, want succeeded (parked run published by the session-start sweep)", run.Status)
	}
	if creates, finds := prRecorder.calls(); creates != 1 || finds != 1 {
		t.Fatalf("PR client calls: creates=%d finds=%d, want one create and one find", creates, finds)
	}
}
