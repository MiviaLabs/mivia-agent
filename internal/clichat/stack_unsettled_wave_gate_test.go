package clichat

// Regression: an unsettled (pending/running) decompose-continuation wave must
// block the final integration run the same way hasMore=true does. Before the
// fix, loadAllStackChunksForDrive set hasMore=false when a wave was skipped,
// which let settleStackIntegrationRunIfReady and driveStack admit the
// integration run while a continuation-wave run was still live.

import (
	"bytes"
	"context"
	"github.com/MiviaLabs/mivia-agent/internal/cliworkflow"
	"io"
	"testing"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

func TestSettleIntegrationRunBlockedByUnsettledWave(t *testing.T) {
	const stackID = "wfr-ugate"
	prepared := seedStackDriveGateFixtureBase(t, stackID)
	_, chunks, _, _, err := parseStackPlanOutput([]byte(multiChunkPlanOutput))
	if err != nil {
		t.Fatal(err)
	}
	ledger := seedMergedChunks(t, prepared, stackID, chunks)

	tests := []struct {
		name             string
		hasMore          bool
		hasUnsettledWave bool
		wantAdmit        bool
	}{
		{
			name:             "unsettled wave blocks integration",
			hasMore:          false,
			hasUnsettledWave: true,
			wantAdmit:        false,
		},
		{
			name:             "has_more blocks integration",
			hasMore:          true,
			hasUnsettledWave: false,
			wantAdmit:        false,
		},
		{
			name:             "no unsettled wave and no has_more admits integration",
			hasMore:          false,
			hasUnsettledWave: false,
			wantAdmit:        true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var admitted bool
			orig := waitIntegrationRunSettledFn
			waitIntegrationRunSettledFn = func(_ context.Context, _ *cliworkflow.PreparedWorkflowRun, _ *workflowledger.Store, _ MergeChecker, _, _ string, _ bool, _, _ io.Writer) error {
				admitted = true
				return nil
			}
			t.Cleanup(func() { waitIntegrationRunSettledFn = orig })

			var stdout, stderr bytes.Buffer
			err := settleStackIntegrationRunIfReady(context.Background(), prepared, ledger, stackID, chunks, tc.hasMore, tc.hasUnsettledWave, &stdout, &stderr)
			if err != nil {
				t.Fatalf("settleStackIntegrationRunIfReady = %v, want no error", err)
			}
			if admitted != tc.wantAdmit {
				t.Fatalf("admitted = %v, want %v (hasMore=%v hasUnsettledWave=%v)", admitted, tc.wantAdmit, tc.hasMore, tc.hasUnsettledWave)
			}
		})
	}
}

// seedMergedChunks seeds the ledger with the chunk plan and transitions every
// chunk task to merged so allChunksMerged and allTasksMerged pass.
func seedMergedChunks(t *testing.T, prepared *cliworkflow.PreparedWorkflowRun, stackID string, chunks []ChunkPlan) *workflowledger.Store {
	t.Helper()
	ledger := workflowledger.NewStore(prepared.Store)
	if err := seedStackLedger(ledger, stackID, chunks); err != nil {
		t.Fatal(err)
	}
	for _, c := range chunks {
		if err := ledger.TransitionTask(stackID, c.ID, stackStatusMerged); err != nil {
			t.Fatalf("transition chunk %s to merged: %v", c.ID, err)
		}
	}
	return ledger
}
