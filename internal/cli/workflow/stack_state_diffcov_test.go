package workflow

// Moved from chat's diffcov2_test.go with stack_state.go.

import (
	"bytes"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

func TestDiffCov2ChunkSettleSucceededNotNoDiff(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	store := workflowledger.NewStore(storage.NewMemory())
	var buf bytes.Buffer
	chunkSettleSucceeded(repo, store, "stk", "chk",
		workflowledger.RunSnapshot{RunID: "r1", Status: workflowledger.RunStatusSucceeded}, &buf)
	chunkSettleSucceeded(repo, store, "stk", "chk2",
		workflowledger.RunSnapshot{RunID: "r2", Status: workflowledger.RunStatusFailed}, &buf)
}
