package cliworkflow

// replaceTestEnv is a package-local helper's helper of
// the same name (worktree_lifecycle_orphan_test.go): a generic os.Environ
// filter/override with no worktree dependency, needed here for
// workflow_kill_recovery_test.go's git-command environment fixtures.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

func replaceTestEnv(current []string, replacements map[string]string) []string {
	result := make([]string, 0, len(current)+len(replacements))
	for _, entry := range current {
		key, _, _ := strings.Cut(entry, "=")
		if _, replace := replacements[key]; !replace {
			result = append(result, entry)
		}
	}
	for key, value := range replacements {
		result = append(result, key+"="+value)
	}
	return result
}

// Min returns the smaller of two ints. It mirrors cli.Min (tui_helpers.go);
// duplicated for tests that bound printed output.
func Min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// parseRunLine extracts the last run_id=/status= pair from multi-line
// `mivia workflow run` progress output. It mirrors cli.parseRunLine
// (stack_command.go).
func parseRunLine(out string) (runID, status string) {
	for _, line := range strings.Split(out, "\n") {
		var lineRunID, lineStatus string
		for _, field := range strings.Fields(line) {
			if strings.HasPrefix(field, "run_id=") {
				lineRunID = strings.TrimPrefix(field, "run_id=")
			}
			if strings.HasPrefix(field, "status=") {
				lineStatus = strings.TrimPrefix(field, "status=")
			}
		}
		if lineRunID != "" {
			runID, status = lineRunID, lineStatus
		}
	}
	return runID, status
}

// wave0DecomposeOutput is the plan run's succeeded decompose output: two
// chunks and has_more=true with a remaining scope. Duplicated from cli's
// stack_drive_recovery_test.go.
const wave0DecomposeOutput = `{"stack_mode":"multi","chunk_plan":{"chunks":[
	{"id":"c1","title":"chunk one","files":["a.go"],"est_diff_lines":20,"tests":true,"depends_on":[]},
	{"id":"c2","title":"chunk two","files":["b.go"],"est_diff_lines":30,"tests":true,"depends_on":["c1"]}
],"has_more":true,"remaining_scope":"the rest of the plan"}}`

// wave1DecomposeOutput closes the stack: two more chunks and has_more=false.
const wave1DecomposeOutput = `{"stack_mode":"multi","chunk_plan":{"chunks":[
	{"id":"c3","title":"chunk three","files":["c.go"],"est_diff_lines":25,"tests":true,"depends_on":[]},
	{"id":"c4","title":"chunk four","files":["d.go"],"est_diff_lines":35,"tests":true,"depends_on":["c3"]}
],"has_more":false}}`

// stackDecomposeContinueKeyLocal derives wave N's stable invocation key. It
// mirrors cli.stackDecomposeContinueKey (stack_decompose_continue.go).
func stackDecomposeContinueKeyLocal(stackID string, wave int) (string, error) {
	if strings.TrimSpace(stackID) == "" {
		return "", fmt.Errorf("stack id must not be empty")
	}
	if wave < 1 {
		return "", fmt.Errorf("decompose continuation wave must be >= 1 (got %d)", wave)
	}
	return fmt.Sprintf("%s:decompose:%d", stackID, wave), nil
}

// newDecomposeRecoveryRepo returns a memory run ledger seeded with the
// stack's succeeded wave-0 decompose output. Duplicated from cli's
// stack_drive_recovery_test.go.
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
// stable invocation key and settles it to the requested status. Duplicated
// from cli's stack_drive_recovery_test.go.
func createContinuationRun(t *testing.T, repo workflowledger.Repository, stackID string, wave int, runID string, status workflowledger.RunStatus, startedAt time.Time) {
	t.Helper()
	ctx := context.Background()
	key, err := stackDecomposeContinueKeyLocal(stackID, wave)
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

// chunkIDs returns the ids of a chunk list in order. Duplicated from cli's
// stack_drive_recovery_test.go.
func chunkIDs(chunks []delivery.ChunkPlan) []string {
	ids := make([]string, len(chunks))
	for i, c := range chunks {
		ids[i] = c.ID
	}
	return ids
}

// chunkIDsEqual reports whether a chunk list carries exactly the given ids
// in order. Duplicated from cli's stack_drive_recovery_test.go.
func chunkIDsEqual(chunks []delivery.ChunkPlan, want ...string) bool {
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

// settleRunToDeliveryPending moves a pending run to delivery_pending through
// the ledger's valid transition chain. Duplicated from cli's
// session_delivery_repair_gate_test.go.
func settleRunToDeliveryPending(t *testing.T, repo workflowledger.Repository, runID string) {
	t.Helper()
	ctx := context.Background()
	stored, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, runID, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	stored, err = repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, runID, stored.Version, workflowledger.RunStatusDeliveryPending, nil); err != nil {
		t.Fatal(err)
	}
}
func seedPlanRunDeliveryPending(t *testing.T, repo workflowledger.Repository, planRunID, digest string) {
	t.Helper()
	ctx := context.Background()
	inputs := map[string]string{"task": "x"}
	rawSnap, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{Inputs: inputs})
	if err != nil {
		t.Fatal(err)
	}
	planRun := workflowledger.RunSnapshot{
		RunID: planRunID, WorkflowName: "mini-stack", WorkflowDigest: digest,
		SnapshotDigest: workflowledger.SnapshotDigest(rawSnap),
		InputDigest:    workflowledger.InputDigest(inputs),
		Status:         workflowledger.RunStatusPending,
	}
	if err := repo.CreateRun(ctx, planRun, rawSnap); err != nil {
		t.Fatal(err)
	}
	for _, next := range []workflowledger.RunStatus{workflowledger.RunStatusRunning, workflowledger.RunStatusDeliveryPending} {
		stored, err := repo.GetRun(ctx, planRunID)
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.CompareAndSetRunStatus(ctx, planRunID, stored.Version, next, nil); err != nil {
			t.Fatal(err)
		}
	}
}
func seedStackDriveIncompleteGateFixture(t *testing.T) (*PreparedWorkflowRun, string) {
	t.Helper()
	const planRunID = "wfr-drive-incomplete-gate"
	return seedStackDriveGateFixtureBase(t, planRunID), planRunID
}
func seedStackDriveGateFixtureBase(t *testing.T, planRunID string) *PreparedWorkflowRun {
	t.Helper()
	root := t.TempDir()
	storePath := filepath.Join(root, "workflow.db")
	writeWorkflowRunFixture(t, root, "https://example.com", storePath)
	miniStackPath := filepath.Join(root, ".mivia", "workflows", "mini-stack.toml")
	if err := os.WriteFile(miniStackPath, []byte(miniStackWorkflowTOML), 0o600); err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareWorkflowRun("mini-stack", root, filepath.Join(root, "config.toml"), []string{"task=x"})
	if err != nil {
		t.Fatalf("PrepareWorkflowRun() error = %v", err)
	}
	t.Cleanup(prepared.CloseFn)

	seedPlanRunDeliveryPending(t, prepared.Repo, planRunID, prepared.Compiled.Digest)
	seedSucceededDecomposeAttempt(t, prepared.Repo, planRunID, []byte(multiChunkPlanOutput))

	_, chunks, _, _, err := ParseStackPlanOutputFunc([]byte(multiChunkPlanOutput))
	if err != nil {
		t.Fatal(err)
	}
	ledger := workflowledger.NewStore(prepared.Store)
	if err := SeedStackLedgerFunc(ledger, planRunID, chunks); err != nil {
		t.Fatal(err)
	}
	return prepared
}
func seedStackDriveFailedGateFixture(t *testing.T) (*PreparedWorkflowRun, string) {
	t.Helper()
	const planRunID = "wfr-drive-failed-gate"
	prepared := seedStackDriveGateFixtureBase(t, planRunID)
	ledger := workflowledger.NewStore(prepared.Store)
	if err := ledger.TransitionTask(planRunID, "c2", delivery.StatusFailed); err != nil {
		t.Fatal(err)
	}
	return prepared, planRunID
}
func writeWorkflowRunFixture(t *testing.T, root, providerURL, storePath string) {
	workflowRoot := filepath.Join(root, ".mivia", "workflows")
	for _, dir := range []string{
		filepath.Join(workflowRoot, "templates"),
		filepath.Join(workflowRoot, "schemas"),
		filepath.Join(root, ".agents", "agents"),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeFile := func(path, body string) {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	config := `[provider]
name = "openrouter"

[providers.openrouter]
base_url = "` + providerURL + `"
api_key_env = "WORKFLOW_TEST_KEY"
models = [{ name = "test/model", context_window_tokens = 128000 }]

[subagents]
max_workers = 1
default_timeout_seconds = 30
store_backend = "sqlite"
store_path = "` + tomlPathLiteral(storePath) + `"
`
	writeFile(filepath.Join(root, "config.toml"), config)
	for _, name := range []string{"one", "two"} {
		writeFile(filepath.Join(root, ".agents", "agents", name+".md"), "---\nname: "+name+"\ndescription: test\ntools: [read_file]\nmax_turns: 1\n---\n")
	}
	writeFile(filepath.Join(workflowRoot, "templates", "one.md"), "Return the result for {{ inputs.task }}.")
	writeFile(filepath.Join(workflowRoot, "templates", "two.md"), "Return the result for {{ evidence.previous }}.")
	writeFile(filepath.Join(workflowRoot, "schemas", "out.json"), `{"type":"object","required":["ok"],"properties":{"ok":{"type":"boolean"}},"additionalProperties":false}`)
	writeFile(filepath.Join(workflowRoot, "two-step.toml"), `version = 1
name = "two-step"
initial_step = "one"

[inputs.task]
type = "string"
required = true
max_bytes = 100

[[steps]]
id = "one"
kind = "agent"
agent = "one"
template = "templates/one.md"
output_schema = "schemas/out.json"
context = [{ from = "inputs.task", as = "task", max_bytes = 100 }, { from = "delivery.failure", as = "delivery_hint", max_bytes = 8192, optional = true }]

[[steps]]
id = "two"
kind = "agent"
agent = "two"
template = "templates/two.md"
output_schema = "schemas/out.json"
context = [{ from = "steps.one.output", as = "previous", max_bytes = 100 }, { from = "delivery.failure", as = "delivery_hint", max_bytes = 8192, optional = true }]

[[transitions]]
from = "one"
to = "two"
[transitions.match]
status = "succeeded"

[[transitions]]
from = "two"
to = "success"
[transitions.match]
status = "succeeded"
`)
	t.Setenv("WORKFLOW_TEST_KEY", "test-key")
}

// setWorkflowAgentTools writes both workflow agents with the given tool.
// Duplicated from cli (workflow_run_integration_test.go).
func setWorkflowAgentTools(t *testing.T, root, tool string) {
	t.Helper()
	for _, name := range []string{"one", "two"} {
		path := filepath.Join(root, ".agents", "agents", name+".md")
		body := "---\nname: " + name + "\ndescription: \"workflow agent\"\ntools:\n  - " + tool + "\nmax_turns: 2\n---\n"
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// initWorkflowGitRepo initializes a git repo with one commit. Duplicated
// from cli (workflow_run_integration_test.go).
func initWorkflowGitRepo(t *testing.T, root string) {
	t.Helper()
	commands := [][]string{{"init", "-b", "main"}, {"config", "user.email", "test@example.com"}, {"config", "user.name", "Test"}, {"add", "."}, {"commit", "-m", "fixture"}}
	for _, args := range commands {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
}
