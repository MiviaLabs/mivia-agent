package cli

// max_total_chunks enforcement on the drive loader: an over-cap stack
// (admission checked the cap only AFTER a continuation run had already been
// admitted, and the error path left the wave's chunks unseeded) used to
// silently exceed the cap on the next drive - loadAllStackChunksForDrive
// replayed every admitted wave's output and seeded them all. The loader now
// refuses an over-cap chunk list so a re-drive halts with an actionable
// error instead of bypassing the cap.

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// TestLoadAllStackChunksForDriveRefusesOverCapStack seeds a stack whose
// admitted waves total more chunks than max_total_chunks: the drive loader
// must refuse the list, not return it for seeding.
func TestLoadAllStackChunksForDriveRefusesOverCapStack(t *testing.T) {
	const stackID = "wfr-stack-overcap"
	repo := newDecomposeRecoveryRepo(t, stackID)
	// Wave 1 already admitted and succeeded: its output adds c3 and c4 to
	// wave 0's c1 and c2 - 4 total against a cap of 3.
	createContinuationRun(t, repo, stackID, 1, "wfr-wave1-ok", workflowledger.RunStatusSucceeded, time.Now().Add(-time.Minute))
	seedSucceededDecomposeAttempt(t, repo, "wfr-wave1-ok", []byte(wave1DecomposeOutput))

	prepared := &preparedWorkflowRun{repo: repo, compiled: &compiler.CompiledWorkflow{
		Stacking: &definition.StackingConfig{Enabled: true, MaxTotalChunks: 3},
	}}
	var stdout, stderr bytes.Buffer
	_, _, _, err := loadAllStackChunksForDrive(prepared, stackID, []byte(wave0DecomposeOutput), map[string]string{"task": "demo"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("loadAllStackChunksForDrive succeeded with 4 chunks against max_total_chunks=3, want a cap error")
	}
	if !strings.Contains(err.Error(), "max_total_chunks=3") {
		t.Fatalf("error = %v, want it to name the cap", err)
	}
}

// TestLoadAllStackChunksForDriveAllowsAtCapStack pins the boundary: exactly
// max_total_chunks chunks load cleanly (the cap is inclusive).
func TestLoadAllStackChunksForDriveAllowsAtCapStack(t *testing.T) {
	const stackID = "wfr-stack-atcap"
	repo := newDecomposeRecoveryRepo(t, stackID)
	createContinuationRun(t, repo, stackID, 1, "wfr-wave1-ok", workflowledger.RunStatusSucceeded, time.Now().Add(-time.Minute))
	seedSucceededDecomposeAttempt(t, repo, "wfr-wave1-ok", []byte(wave1DecomposeOutput))

	prepared := &preparedWorkflowRun{repo: repo, compiled: &compiler.CompiledWorkflow{
		Stacking: &definition.StackingConfig{Enabled: true, MaxTotalChunks: 4},
	}}
	var stdout, stderr bytes.Buffer
	chunks, _, _, err := loadAllStackChunksForDrive(prepared, stackID, []byte(wave0DecomposeOutput), map[string]string{"task": "demo"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("loadAllStackChunksForDrive = %v, want no error at exactly the cap", err)
	}
	if !chunkIDsEqual(chunks, "c1", "c2", "c3", "c4") {
		t.Fatalf("chunks = %v, want all four at exactly the cap", chunkIDs(chunks))
	}
}
