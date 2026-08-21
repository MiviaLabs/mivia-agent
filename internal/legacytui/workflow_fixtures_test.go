package legacytui

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// The fixtures in this file are package-local copies of internal/cli's test
// fixtures of the same name (workflow_events_test.go, workflow_next_step_test.go,
// workflow_ops_command_test.go, workflow_run_integration_test.go): Go test
// files are not part of a package's importable surface, so a fixture chain
// shared by tests in both packages must exist in each.

// testWorkflowDefinition is a minimal linear workflow: start -> review ->
// success. The file name and in-file name are both "test-wf", matching the
// runs the event fixture seeds.
const testWorkflowDefinition = `version = 1
name = "test-wf"
description = "Runs the test workflow."
initial_step = "start"

[[steps]]
id = "start"
kind = "agent"
agent = "test-agent"

[[steps]]
id = "review"
kind = "agent"
agent = "test-agent"

[[transitions]]
from = "start"
to = "review"
match = { status = "succeeded" }

[[transitions]]
from = "review"
to = "success"
match = { status = "succeeded" }
`

// writeWorkflowDefinition writes a workflow definition beneath the workspace
// .mivia/workflows directory.
func writeWorkflowDefinition(t *testing.T, root, name, body string) {
	t.Helper()
	dir := filepath.Join(root, ".mivia", "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// openEventsFixtureWithRun builds a workspace config + sqlite store + a fresh
// run with the given runID, mirroring how OpenWorkflowReportContext resolves
// them, and returns the store handles so the caller can seed events.
func openEventsFixtureWithRun(t *testing.T, runID string) (root string, store *storage.SQLite, repo *workflowledger.StorageRepository, closeFn func(), ctx context.Context, run string) {
	t.Helper()
	root = t.TempDir()
	configPath := filepath.Join(root, "config.toml")
	configBody := `[provider]
name = "openrouter"
[providers.openrouter]
base_url = "https://example.com"
api_key_env = "WORKFLOW_EVENTS_TEST_KEY"
models = [{ name = "test/model", context_window_tokens = 128000 }]
[subagents]
store_backend = "sqlite"
`
	if err := os.WriteFile(configPath, []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}

	work, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	res, err := config.Load(config.LoadOptions{ConfigPath: configPath})
	if err != nil {
		t.Fatal(err)
	}
	cli.ApplyPrivacyPolicy(res)
	cli.ApplyWorkflowStoreRoot(res, work.Abs)
	store, _, closeFn, err = cli.OpenWorkflowStore(work.Abs, res.Subagents)
	if err != nil {
		t.Fatal(err)
	}
	repo = workflowledger.NewStorageRepository(store)

	ctx = context.Background()
	run = runID
	snap := workflowledger.RunSnapshot{
		RunID: run, WorkflowName: "test-wf", Status: workflowledger.RunStatusPending, ActiveStepID: "start",
	}
	snapshotJSON, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{
		SchemaVersion: 1, DefinitionTOML: []byte("x"), DefinitionDigest: "d",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateRun(ctx, snap, snapshotJSON); err != nil {
		t.Fatal(err)
	}
	return root, store, repo, closeFn, ctx, run
}

// gatedWorkflowDefinition is a two-step workflow: an agent step "one" routes
// to a human_gate step "review".
const gatedWorkflowDefinition = `version = 1
name = "gated"
initial_step = "one"

[inputs.task]
type = "string"
required = true
max_bytes = 100

[[steps]]
id = "one"
kind = "agent"
agent = "one"

[[steps]]
id = "review"
kind = "human_gate"

[[transitions]]
from = "one"
to = "review"
[transitions.match]
status = "succeeded"

[[transitions]]
from = "review"
to = "success"
[transitions.match]
status = "succeeded"
`

// writeWorkflowRunFixture writes a workspace config, two agent definitions,
// and a "two-step" workflow definition (templates, schemas), the base
// infrastructure newGatedApprovalWorkspace layers its gated workflow on top of.
func writeWorkflowRunFixture(t *testing.T, root, providerURL, storePath string) {
	workflowRoot := filepath.Join(root, ".mivia", "workflows")
	for _, dir := range []string{
		filepath.Join(workflowRoot, "templates"),
		filepath.Join(workflowRoot, "schemas"),
		filepath.Join(root, ".mivia", "agents"),
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
	cfg := `[provider]
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
	writeFile(filepath.Join(root, "config.toml"), cfg)
	for _, name := range []string{"one", "two"} {
		writeFile(filepath.Join(root, ".mivia", "agents", name+".toml"), `name = "`+name+`"
description = "workflow test agent"
tools = ["read_file"]
max_turns = 1
`)
	}
	writeFile(filepath.Join(workflowRoot, "templates", "one.md"), "Return the result for {{ inputs.task }}.")
	writeFile(filepath.Join(workflowRoot, "templates", "two.md"), "Return the result for {{ evidence.previous }}.")
	writeFile(filepath.Join(workflowRoot, "schemas", "out.json"), `{"type":"object","required":["ok"],"properties":{"ok":{"type":"boolean"}},"additionalProperties":false}`)
}

// newGatedApprovalWorkspace builds the workspace, config, and the immutable
// run record for the gated approval fixture, before any history is seeded.
func newGatedApprovalWorkspace(t *testing.T) (root, configPath, storePath string, raw []byte, run workflowledger.RunSnapshot) {
	t.Helper()
	root = t.TempDir()
	storePath = filepath.Join(root, "workflow.db")
	t.Setenv("MIVIA_ALLOW_INSECURE_HTTP", "1")
	writeWorkflowRunFixture(t, root, "http://127.0.0.1:1", storePath)
	configPath = filepath.Join(root, "config.toml")
	if err := os.WriteFile(filepath.Join(root, ".mivia", "workflows", "gated.toml"), []byte(gatedWorkflowDefinition), 0o600); err != nil {
		t.Fatal(err)
	}
	wf, _, err := definition.ParseWorkflowTOML([]byte(gatedWorkflowDefinition), "gated.toml")
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := definition.Compile(&wf)
	if err != nil {
		t.Fatal(err)
	}
	skillReg, err := cli.LoadChatSkills(root)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := cli.LoadAgentDefinitions(root, "", skillReg)
	if err != nil {
		t.Fatal(err)
	}
	agentDef, ok := loaded.Registry.Get("one")
	if !ok {
		t.Fatal("agent one is missing")
	}
	digest, err := agentDef.DefinitionDigest()
	if err != nil {
		t.Fatal(err)
	}
	snapshot := workflowledger.Snapshot{
		SchemaVersion:  workflowledger.SnapshotSchemaVersion,
		DefinitionTOML: []byte(gatedWorkflowDefinition), DefinitionDigest: compiled.Digest,
		Inputs: map[string]string{"task": "test"},
		Agents: map[string]workflowledger.AgentSnapshot{"one": {Digest: digest}},
	}
	raw, err = workflowledger.MarshalSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	run = workflowledger.RunSnapshot{
		RunID: "wfr-gated-approval", WorkflowName: compiled.Name, WorkflowDigest: compiled.Digest,
		SnapshotDigest: workflowledger.SnapshotDigest(raw),
		InputDigest:    workflowledger.InputDigest(snapshot.Inputs),
		Status:         workflowledger.RunStatusPending, ActiveStepID: compiled.InitialStep,
	}
	return root, configPath, storePath, raw, run
}

// seedGatedApprovalHistory persists the run and its parked history: the
// agent step succeeded, then the human gate parks at waiting_approval.
func seedGatedApprovalHistory(t *testing.T, storePath string, raw []byte, run workflowledger.RunSnapshot) {
	t.Helper()
	store, err := cli.OpenContextStorePath(storePath)
	if err != nil {
		t.Fatal(err)
	}
	repo := workflowledger.NewStorageRepository(store)
	ctx := context.Background()
	if err := repo.CreateRun(ctx, run, raw); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := parkGatedAgentStep(ctx, repo, run.RunID); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := parkGatedHumanGate(ctx, repo, run.RunID); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

// parkGatedAgentStep completes step "one" with a route to the human gate and
// parks the run at waiting_approval.
func parkGatedAgentStep(ctx context.Context, repo workflowledger.Repository, runID string) error {
	stored, err := repo.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	if err := repo.CompareAndSetRunStatus(ctx, runID, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		return err
	}
	attempt := workflowledger.StepAttempt{AttemptID: "att-one", RunID: runID, StepID: "one", AttemptNo: 1, Status: workflowledger.AttemptStatusPending}
	if err := repo.CreateStepAttempt(ctx, attempt); err != nil {
		return err
	}
	storedAttempt, err := repo.GetStepAttempt(ctx, runID, attempt.AttemptID)
	if err != nil {
		return err
	}
	if err := repo.CompleteStepAttempt(ctx, runID, attempt.AttemptID, storedAttempt.Version, workflowledger.AttemptOutcome{
		Status: workflowledger.AttemptStatusSucceeded, ToStepID: "review",
	}); err != nil {
		return err
	}
	stored, err = repo.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	return repo.CompareAndSetRunStatus(ctx, runID, stored.Version, workflowledger.RunStatusWaitingApproval, nil)
}

// parkGatedHumanGate creates the human-gate attempt and its pending approval.
func parkGatedHumanGate(ctx context.Context, repo workflowledger.Repository, runID string) error {
	attempt := workflowledger.StepAttempt{AttemptID: "att-review", RunID: runID, StepID: "review", AttemptNo: 1, Status: workflowledger.AttemptStatusPending}
	if err := repo.CreateStepAttempt(ctx, attempt); err != nil {
		return err
	}
	return repo.CreateApproval(ctx, workflowledger.ApprovalRecord{
		ApprovalID: "wfa-approval-review-1", RunID: runID, StepID: "review", Status: "pending",
	})
}

// newGatedApprovalFixture seeds a real store with a run parked at
// waiting_approval on human_gate step "review" (after a succeeded agent step
// "one"), the state an interrupted executor leaves behind.
func newGatedApprovalFixture(t *testing.T) (root, configPath, storePath, runID string) {
	t.Helper()
	root, configPath, storePath, raw, run := newGatedApprovalWorkspace(t)
	seedGatedApprovalHistory(t, storePath, raw, run)
	return root, configPath, storePath, run.RunID
}

// openWorkflowTestStore reopens the fixture store read-only for assertions.
func openWorkflowTestStore(t *testing.T, storePath string) workflowledger.Repository {
	t.Helper()
	store, err := cli.OpenContextStorePath(storePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return workflowledger.NewStorageRepository(store)
}
