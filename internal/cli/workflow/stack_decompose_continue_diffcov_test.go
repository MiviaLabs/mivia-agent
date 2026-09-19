package workflow

// Moved from chat's diffcov2_test.go with the stack decompose-continue code.

import (
	"io"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

func TestDiffCov2AdmitNextWaveHaltedAtMaxTotal(t *testing.T) {
	store := workflowledger.NewStore(storage.NewMemory())
	chunks := []ChunkPlan{{ID: "c1", Title: "one"}}
	if err := seedStackLedger(store, "stk-max", chunks); err != nil {
		t.Fatal(err)
	}
	if err := store.TransitionTask("stk-max", "c1", stackStatusMerged); err != nil {
		t.Fatal(err)
	}
	prepared := &PreparedWorkflowRun{
		Repo: workflowledger.NewMemoryRepository(),
		Compiled: &definition.CompiledWorkflow{
			Stacking: &definition.StackingConfig{MaxTotalChunks: 1},
		},
	}
	err := admitNextWaveIfReady(prepared, store, "stk-max", chunks, true, "more scope", nil, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "max_total_chunks") {
		t.Fatalf("admitNextWaveIfReady(cap reached) err = %v; want max_total_chunks halt", err)
	}
}
