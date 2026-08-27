package clichat

// Regression tests for the decompose-continuation wave wedge (BUG 5): a
// failed or crashed wave-1+ run used to make loadAllStackChunks fail with
// 'plan run %q has no succeeded decompose output' BEFORE any admission, so
// the whole stack - including already-mergeable wave-0 chunks - could never
// be driven again. loadAllStackChunksForDrive recovers per wave instead:
// a terminal-failed wave is re-admitted with a fresh run under the same
// stable key (newest-wins run-ref lookup), a wave whose chunks were already
// produced by an older succeeded run under the key is replayed from that
// output, and a still-live (pending/running) wave is skipped with an
// actionable message naming the run id - never the bare wedge error.

import (
	"bytes"
	"context"
	"errors"
	"github.com/MiviaLabs/mivia-agent/internal/cliworkflow"
	"io"
	"strings"
	"testing"
	"time"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// wave0DecomposeOutput is the plan run's succeeded decompose output: two
// chunks and has_more=true with a remaining scope (parseStackPlanOutput
// requires remaining_scope when has_more=true).
const wave0DecomposeOutput = `{"stack_mode":"multi","chunk_plan":{"chunks":[
	{"id":"c1","title":"chunk one","files":["a.go"],"est_diff_lines":20,"tests":true,"depends_on":[]},
	{"id":"c2","title":"chunk two","files":["b.go"],"est_diff_lines":30,"tests":true,"depends_on":["c1"]}
],"has_more":true,"remaining_scope":"the rest of the plan"}}`

// wave1DecomposeOutput closes the stack: two more chunks and has_more=false.
const wave1DecomposeOutput = `{"stack_mode":"multi","chunk_plan":{"chunks":[
	{"id":"c3","title":"chunk three","files":["c.go"],"est_diff_lines":25,"tests":true,"depends_on":[]},
	{"id":"c4","title":"chunk four","files":["d.go"],"est_diff_lines":35,"tests":true,"depends_on":["c3"]}
],"has_more":false}}`

// newDecomposeRecoveryRepo returns a memory run ledger seeded with the
// stack's succeeded wave-0 decompose output. The plan run exists as a real
// run whose RunID is the stack id (exactly how runStackDrive resolves it,
// and how loadStackPlanOutput keys the wave-0 output).
func newDecomposeRecoveryRepo(t *testing.T, stackID string) workflowledger.Repository {
	t.Helper()
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	snap := workflowledger.RunSnapshot{
		RunID: stackID, WorkflowName: "mini-stack", Status: workflowledger.RunStatusPending,
	}
	if err := repo.CreateRun(ctx, snap, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	seedSucceededDecomposeAttempt(t, repo, stackID, []byte(wave0DecomposeOutput))
	return repo
}

// createContinuationRun admits a decompose-continuation run under wave N's
// stable invocation key and settles it to the requested status (via the
// ledger's valid transition chain pending -> running -> status). StartedAt is
// honored so tests can control which run the newest-wins lookup picks.
func createContinuationRun(t *testing.T, repo workflowledger.Repository, stackID string, wave int, runID string, status workflowledger.RunStatus, startedAt time.Time) {
	t.Helper()
	ctx := context.Background()
	key, err := stackDecomposeContinueKey(stackID, wave)
	if err != nil {
		t.Fatal(err)
	}
	snap := workflowledger.RunSnapshot{
		RunID: runID, InvocationKey: key, WorkflowName: "mini-stack",
		Status: workflowledger.RunStatusPending, StartedAt: startedAt,
	}
	if err := repo.CreateRun(ctx, snap, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if status == workflowledger.RunStatusPending {
		return
	}
	stored, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, runID, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	if status == workflowledger.RunStatusRunning {
		return
	}
	current, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, runID, current.Version, status, nil); err != nil {
		t.Fatal(err)
	}
}

// stubAdmission swaps the admission seam for the test and restores it on
// cleanup.
func stubAdmission(t *testing.T, fn func(*cliworkflow.PreparedWorkflowRun, string, int, string, map[string]string, io.Writer, io.Writer) ([]ChunkPlan, bool, string, error)) {
	t.Helper()
	original := stackDecomposeContinueAdmit
	stackDecomposeContinueAdmit = fn
	t.Cleanup(func() { stackDecomposeContinueAdmit = original })
}

// chunkIDs returns the ids of a chunk list in order.
func chunkIDs(chunks []ChunkPlan) []string {
	ids := make([]string, len(chunks))
	for i, c := range chunks {
		ids[i] = c.ID
	}
	return ids
}

// chunkIDsEqual reports whether a chunk list carries exactly the given ids
// in order.
func chunkIDsEqual(chunks []ChunkPlan, want ...string) bool {
	got := chunkIDs(chunks)
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestLoadAllStackChunksForDriveReAdmitsFailedContinuationWave pins the
// primary recovery: a wave whose newest run settled failed (decompose agent
// refusal or invalid plan output) is re-admitted with a fresh run under the
// same stable key, and the drive proceeds with every wave's chunks - the
// failed run no longer wedges the stack.
func TestLoadAllStackChunksForDriveReAdmitsFailedContinuationWave(t *testing.T) {
	const stackID = "wfr-stack-readmit"
	repo := newDecomposeRecoveryRepo(t, stackID)
	createContinuationRun(t, repo, stackID, 1, "wfr-wave1-failed", workflowledger.RunStatusFailed, time.Now().Add(-time.Minute))

	var admitted int
	var admittedWave int
	var admittedScope string
	stubAdmission(t, func(_ *cliworkflow.PreparedWorkflowRun, stackID string, wave int, remainingScope string, _ map[string]string, stdout, _ io.Writer) ([]ChunkPlan, bool, string, error) {
		admitted++
		admittedWave = wave
		admittedScope = remainingScope
		// The fresh re-admitted run lands under the same key and succeeds,
		// so the newest-wins run-ref lookup resolves to it next time.
		createContinuationRun(t, repo, stackID, wave, "wfr-wave1-readmit", workflowledger.RunStatusSucceeded, time.Now())
		seedSucceededDecomposeAttempt(t, repo, "wfr-wave1-readmit", []byte(wave1DecomposeOutput))
		_, chunks, hasMore, remaining, err := parseStackPlanOutput([]byte(wave1DecomposeOutput))
		if err != nil {
			t.Fatal(err)
		}
		return chunks, hasMore, remaining, nil
	})

	var stdout, stderr bytes.Buffer
	chunks, hasMore, _, remaining, err := loadAllStackChunksForDrive(&cliworkflow.PreparedWorkflowRun{Repo: repo}, stackID, []byte(wave0DecomposeOutput), map[string]string{"task": "demo"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("loadAllStackChunksForDrive = %v, want no error (failed wave must be re-admitted, not wedged)", err)
	}
	if !chunkIDsEqual(chunks, "c1", "c2", "c3", "c4") {
		t.Fatalf("chunks = %v, want [c1 c2 c3 c4]", chunkIDs(chunks))
	}
	if hasMore {
		t.Fatalf("hasMore = true, want false (wave 1 closed the stack)")
	}
	if remaining != "" {
		t.Fatalf("remaining = %q, want empty", remaining)
	}
	if admitted != 1 || admittedWave != 1 {
		t.Fatalf("admission called %d time(s) for wave %d, want exactly once for wave 1", admitted, admittedWave)
	}
	if admittedScope != "the rest of the plan" {
		t.Fatalf("admission remaining_scope = %q, want the wave-0 remaining scope", admittedScope)
	}
	newest, found, err := stackDecomposeContinueRunRef(repo, stackID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !found || newest.RunID != "wfr-wave1-readmit" {
		t.Fatalf("newest wave-1 run = %+v (found=%v), want the re-admitted wfr-wave1-readmit (newest wins)", newest, found)
	}
	if _, err := loadStackPlanOutput(repo, newest.RunID); err != nil {
		t.Fatalf("re-admitted run's decompose output is not loadable: %v", err)
	}
}

// TestLoadAllStackChunksForDriveFailedWaveReAdmissionErrorIsActionable pins
// the fallback: when re-admission itself fails, the drive must NOT wedge -
// it prints an actionable message naming the failed run (resume/delete
// guidance) and still returns the other waves' chunks.
func TestLoadAllStackChunksForDriveFailedWaveReAdmissionErrorIsActionable(t *testing.T) {
	const stackID = "wfr-stack-readmit-fail"
	repo := newDecomposeRecoveryRepo(t, stackID)
	createContinuationRun(t, repo, stackID, 1, "wfr-wave1-failed", workflowledger.RunStatusFailed, time.Now())

	stubAdmission(t, func(*cliworkflow.PreparedWorkflowRun, string, int, string, map[string]string, io.Writer, io.Writer) ([]ChunkPlan, bool, string, error) {
		return nil, false, "", errors.New("sentinel re-admission failure")
	})

	var stdout, stderr bytes.Buffer
	chunks, hasMore, hasUnsettledWave, _, err := loadAllStackChunksForDrive(&cliworkflow.PreparedWorkflowRun{Repo: repo}, stackID, []byte(wave0DecomposeOutput), map[string]string{"task": "demo"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("loadAllStackChunksForDrive = %v, want no hard error (re-admission failure must be actionable, not a wedge)", err)
	}
	if !chunkIDsEqual(chunks, "c1", "c2") {
		t.Fatalf("chunks = %v, want the wave-0 chunks [c1 c2] to remain drivable", chunkIDs(chunks))
	}
	if !hasMore {
		t.Fatalf("hasMore = false, want true (wave-0 declared more scope)")
	}
	if !hasUnsettledWave {
		t.Fatalf("hasUnsettledWave = false, want true (wave was skipped)")
	}
	msg := stderr.String()
	for _, want := range []string{"wfr-wave1-failed", "resume or delete", "sentinel re-admission failure"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("stderr = %q, want it to name the failed run and the failure reason (contains %q)", msg, want)
		}
	}
}

// TestLoadAllStackChunksForDrivePendingWaveSkipsWithActionableMessage pins
// the crashed-run recovery: a wave run that is still pending/running (the
// process died mid-run; the run is resumable) must not wedge the drive - it
// is skipped with a message naming the run id and its resumability, and the
// rest of the stack stays drivable.
func TestLoadAllStackChunksForDrivePendingWaveSkipsWithActionableMessage(t *testing.T) {
	const stackID = "wfr-stack-pending"
	repo := newDecomposeRecoveryRepo(t, stackID)
	createContinuationRun(t, repo, stackID, 1, "wfr-wave1-pending", workflowledger.RunStatusPending, time.Now())

	stubAdmission(t, func(*cliworkflow.PreparedWorkflowRun, string, int, string, map[string]string, io.Writer, io.Writer) ([]ChunkPlan, bool, string, error) {
		t.Fatal("admission must not run for a still-pending wave")
		return nil, false, "", nil
	})

	var stdout, stderr bytes.Buffer
	chunks, hasMore, hasUnsettledWave, _, err := loadAllStackChunksForDrive(&cliworkflow.PreparedWorkflowRun{Repo: repo}, stackID, []byte(wave0DecomposeOutput), map[string]string{"task": "demo"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("loadAllStackChunksForDrive = %v, want no hard error (pending wave must be skipped, not wedged)", err)
	}
	if !chunkIDsEqual(chunks, "c1", "c2") {
		t.Fatalf("chunks = %v, want the wave-0 chunks [c1 c2] to remain drivable", chunkIDs(chunks))
	}
	if !hasMore {
		t.Fatalf("hasMore = false, want true (wave-0 declared more scope)")
	}
	if !hasUnsettledWave {
		t.Fatalf("hasUnsettledWave = false, want true (wave was skipped)")
	}
	msg := stderr.String()
	for _, want := range []string{"wfr-wave1-pending", "resumable", "mivia workflow resume"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("stderr = %q, want it to name the resumable run and the resume command (contains %q)", msg, want)
		}
	}
}

// TestLoadAllStackChunksForDriveReplaysAlreadySeededWave pins the seeded-wave
// recovery: when a newer wave run settled failed but an older succeeded run
// under the same key already produced the wave's chunks (durable, seeded
// data), the drive replays that output instead of re-admitting - no new run,
// no wedge, and the run ledger is untouched.
func TestLoadAllStackChunksForDriveReplaysAlreadySeededWave(t *testing.T) {
	const stackID = "wfr-stack-seeded"
	repo := newDecomposeRecoveryRepo(t, stackID)
	older := time.Now().Add(-time.Hour)
	createContinuationRun(t, repo, stackID, 1, "wfr-wave1-old", workflowledger.RunStatusSucceeded, older)
	seedSucceededDecomposeAttempt(t, repo, "wfr-wave1-old", []byte(wave1DecomposeOutput))
	createContinuationRun(t, repo, stackID, 1, "wfr-wave1-failed", workflowledger.RunStatusFailed, older.Add(time.Minute))

	stubAdmission(t, func(*cliworkflow.PreparedWorkflowRun, string, int, string, map[string]string, io.Writer, io.Writer) ([]ChunkPlan, bool, string, error) {
		t.Fatal("admission must not run when the wave's chunks are already produced by an older succeeded run")
		return nil, false, "", nil
	})

	var stdout, stderr bytes.Buffer
	chunks, hasMore, _, _, err := loadAllStackChunksForDrive(&cliworkflow.PreparedWorkflowRun{Repo: repo}, stackID, []byte(wave0DecomposeOutput), map[string]string{"task": "demo"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("loadAllStackChunksForDrive = %v, want no error (seeded wave must be replayed, not wedged)", err)
	}
	if !chunkIDsEqual(chunks, "c1", "c2", "c3", "c4") {
		t.Fatalf("chunks = %v, want [c1 c2 c3 c4]", chunkIDs(chunks))
	}
	if hasMore {
		t.Fatalf("hasMore = true, want false (wave 1 closed the stack)")
	}
	if msg := stderr.String(); msg != "" {
		t.Fatalf("stderr = %q, want empty for a clean seeded replay", msg)
	}
	runs, err := repo.ListRuns(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, r := range runs {
		if r.InvocationKey == stackID+decomposeContinuePrefix+"1" {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("runs under the wave-1 key = %d, want 2 (replay must not add a run)", count)
	}
}

// TestLoadAllStackChunksForDriveHealthyContinuationWaveUnchanged pins the
// non-regression case: a wave whose newest run has a succeeded decompose
// output loads exactly as before - no warnings, no re-admission.
func TestLoadAllStackChunksForDriveHealthyContinuationWaveUnchanged(t *testing.T) {
	const stackID = "wfr-stack-healthy"
	repo := newDecomposeRecoveryRepo(t, stackID)
	createContinuationRun(t, repo, stackID, 1, "wfr-wave1-ok", workflowledger.RunStatusSucceeded, time.Now())
	seedSucceededDecomposeAttempt(t, repo, "wfr-wave1-ok", []byte(wave1DecomposeOutput))

	stubAdmission(t, func(*cliworkflow.PreparedWorkflowRun, string, int, string, map[string]string, io.Writer, io.Writer) ([]ChunkPlan, bool, string, error) {
		t.Fatal("admission must not run for a healthy wave")
		return nil, false, "", nil
	})

	var stdout, stderr bytes.Buffer
	chunks, hasMore, _, _, err := loadAllStackChunksForDrive(&cliworkflow.PreparedWorkflowRun{Repo: repo}, stackID, []byte(wave0DecomposeOutput), map[string]string{"task": "demo"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("loadAllStackChunksForDrive = %v, want no error", err)
	}
	if !chunkIDsEqual(chunks, "c1", "c2", "c3", "c4") {
		t.Fatalf("chunks = %v, want [c1 c2 c3 c4]", chunkIDs(chunks))
	}
	if hasMore {
		t.Fatalf("hasMore = true, want false (wave 1 closed the stack)")
	}
	if msg := stderr.String(); msg != "" {
		t.Fatalf("stderr = %q, want empty for a healthy wave", msg)
	}
}
