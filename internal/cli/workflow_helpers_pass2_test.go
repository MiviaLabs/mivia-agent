package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/vcs"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

func TestExecuteWorkflowResumeRecoversUnclaimedNonterminalRun(t *testing.T) {
	root, run := newForcedResumeFixture(t)
	configPath := filepath.Join(root, "config.toml")
	var stdout bytes.Buffer
	if err := executeWorkflowResume(run.RunID, root, configPath, false, &stdout, io.Discard); err != nil {
		t.Fatalf("executeWorkflowResume() error = %v", err)
	}
	if !strings.Contains(stdout.String(), "status=succeeded") {
		t.Fatalf("executeWorkflowResume() output = %q", stdout.String())
	}
}

func newForcedResumeFixture(t *testing.T) (string, workflowledger.RunSnapshot) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"{\"ok\":true}"}}]}`)
	}))
	t.Cleanup(server.Close)
	root := t.TempDir()
	storePath := filepath.Join(root, "workflow.db")
	t.Setenv("MIVIA_ALLOW_INSECURE_HTTP", "1")
	writeWorkflowRunFixture(t, root, server.URL, storePath)
	compiled, rawDefinition := compileResumeWorkflowFixture(t, root)
	snapshot := newForcedResumeSnapshot(t, root, compiled, rawDefinition)
	rawSnapshot, err := workflowledger.MarshalSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	run := workflowledger.RunSnapshot{
		RunID: "wfr-resume-force", WorkflowName: compiled.Name, WorkflowDigest: compiled.Digest,
		SnapshotDigest: workflowledger.SnapshotDigest(rawSnapshot),
		InputDigest:    workflowledger.InputDigest(snapshot.Inputs),
		Status:         workflowledger.RunStatusPending, ActiveStepID: compiled.InitialStep,
	}
	store, err := openContextStorePath(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := workflowledger.NewStorageRepository(store).CreateRun(t.Context(), run, rawSnapshot); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	return root, run
}

func compileResumeWorkflowFixture(t *testing.T, root string) (*compiler.CompiledWorkflow, []byte) {
	t.Helper()
	rawDefinition, err := os.ReadFile(filepath.Join(root, ".mivia", "workflows", "two-step.toml"))
	if err != nil {
		t.Fatal(err)
	}
	wf, _, err := definition.ParseWorkflowTOML(rawDefinition, "two-step.toml")
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile(&wf)
	if err != nil {
		t.Fatal(err)
	}
	return compiled, rawDefinition
}

func newForcedResumeSnapshot(t *testing.T, root string, compiled *compiler.CompiledWorkflow, rawDefinition []byte) workflowledger.Snapshot {
	t.Helper()
	skills, err := loadChatSkills(root)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := loadAgentDefinitions(root, "", skills)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := workflowledger.Snapshot{
		SchemaVersion:  workflowledger.SnapshotSchemaVersion,
		DefinitionTOML: rawDefinition, DefinitionDigest: compiled.Digest,
		Inputs: map[string]string{"task": "test"},
		Agents: map[string]workflowledger.AgentSnapshot{},
	}
	for _, name := range []string{"one", "two"} {
		agent, ok := loaded.Registry.Get(name)
		if !ok {
			t.Fatalf("agent %q is missing", name)
		}
		digest, digestErr := agent.DefinitionDigest()
		if digestErr != nil {
			t.Fatal(digestErr)
		}
		snapshot.Agents[name] = workflowledger.AgentSnapshot{
			Digest: digest, ProviderName: "openrouter", Model: "test/model",
		}
	}
	addForcedResumeReferences(t, root, compiled, &snapshot)
	return snapshot
}

func addForcedResumeReferences(t *testing.T, root string, compiled *compiler.CompiledWorkflow, snapshot *workflowledger.Snapshot) {
	t.Helper()
	for _, step := range compiled.Steps {
		if step.Template != "" {
			data, readErr := os.ReadFile(filepath.Join(root, ".mivia", "workflows", step.Template))
			if readErr != nil {
				t.Fatal(readErr)
			}
			if snapshot.Templates == nil {
				snapshot.Templates = map[string]workflowledger.RefSnapshot{}
			}
			snapshot.Templates[step.Template] = workflowledger.RefSnapshot{Digest: digestBytes(data), Bytes: data}
		}
		if step.OutputSchema != "" {
			data, readErr := os.ReadFile(filepath.Join(root, ".mivia", "workflows", step.OutputSchema))
			if readErr != nil {
				t.Fatal(readErr)
			}
			if snapshot.Schemas == nil {
				snapshot.Schemas = map[string]workflowledger.RefSnapshot{}
			}
			snapshot.Schemas[step.OutputSchema] = workflowledger.RefSnapshot{Digest: digestBytes(data), Bytes: data}
		}
	}
}

func TestExecuteWorkflowResumeEarlyErrors(t *testing.T) {
	if err := executeWorkflowResume("wfr-test", filepath.Join(t.TempDir(), "missing"), "", false, io.Discard, io.Discard); err == nil {
		t.Fatal("executeWorkflowResume() error = nil for a missing workspace")
	}
	root := t.TempDir()
	badConfig := filepath.Join(root, "bad.toml")
	if err := os.WriteFile(badConfig, []byte("bad = ["), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := executeWorkflowResume("wfr-test", root, badConfig, false, io.Discard, io.Discard); err == nil {
		t.Fatal("executeWorkflowResume() error = nil for invalid config")
	}
}

func TestReconcileWorkflowTerminalRepairsDerivedStatus(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	run := workflowledger.RunSnapshot{
		RunID: "wfr-derived-terminal", Status: workflowledger.RunStatusPending, ActiveStepID: "one",
	}
	if err := repo.CreateRun(ctx, run, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, run.RunID, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	attempt := workflowledger.StepAttempt{AttemptID: "att-one", RunID: run.RunID, StepID: "one", AttemptNo: 1}
	if err := repo.CreateStepAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	storedAttempt, err := repo.GetStepAttempt(ctx, run.RunID, attempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	outcome := workflowledger.AttemptOutcome{
		Status: workflowledger.AttemptStatusSucceeded, ToStepID: "success", MatchDigest: "match",
	}
	if err := repo.CompleteStepAttempt(ctx, run.RunID, attempt.AttemptID, storedAttempt.Version, outcome); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	terminal, err := reconcileWorkflowTerminal(ctx, repo, run.RunID, false, &stdout)
	if err != nil || !terminal {
		t.Fatalf("reconcileWorkflowTerminal() = %v, %v", terminal, err)
	}
	stored, err = repo.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != workflowledger.RunStatusSucceeded || !strings.Contains(stdout.String(), "status=succeeded") {
		t.Fatalf("run = %+v, output = %q", stored, stdout.String())
	}
}

func TestWorkflowExecutionLockFilesystemErrors(t *testing.T) {
	root := t.TempDir()
	parentFile := filepath.Join(root, "file")
	if err := os.WriteFile(parentFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := acquireWorkflowExecutionLock(filepath.Join(parentFile, "store.db"), "wfr-test"); err == nil {
		t.Fatal("acquireWorkflowExecutionLock() error = nil for a file lock root")
	}
	storePath := filepath.Join(root, "store.db")
	lockDir := filepath.Join(root, workflowExecutionLockDir)
	if err := os.WriteFile(lockDir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := acquireWorkflowExecutionLock(storePath, "wfr-test"); err == nil {
		t.Fatal("acquireWorkflowExecutionLock() error = nil for a lock directory file")
	}
	if err := os.Remove(lockDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(lockDir, 0o700); err != nil {
		t.Fatal(err)
	}
	_, identity, err := workflowStoreLockIdentity(storePath)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("workflow-%x.lock", sha256.Sum256([]byte(identity+"\x00"+"wfr-dir")))
	if err := os.Mkdir(filepath.Join(lockDir, name), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := acquireWorkflowExecutionLock(storePath, "wfr-dir"); err == nil {
		t.Fatal("acquireWorkflowExecutionLock() error = nil for a directory lock file")
	}
	if finish, err := beginWorkflowExecution(root, filepath.Join(root, "missing", "store.db"), "wfr-test"); err == nil {
		finish()
		t.Fatal("beginWorkflowExecution() error = nil for a missing store directory")
	}
}

func TestExecuteWorkflowRunEarlyErrors(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MIVIA_CONFIG", "")
	if err := executeWorkflowRun("test", filepath.Join(t.TempDir(), "missing"), "", nil, false, io.Discard, io.Discard); err == nil {
		t.Fatal("executeWorkflowRun() error = nil for a missing workspace")
	}
	root := t.TempDir()
	badConfig := filepath.Join(root, "bad.toml")
	if err := os.WriteFile(badConfig, []byte("bad = ["), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := executeWorkflowRun("test", root, badConfig, nil, false, io.Discard, io.Discard); err == nil {
		t.Fatal("executeWorkflowRun() error = nil for invalid config")
	}
	if err := os.MkdirAll(filepath.Join(root, ".mivia", "workflows"), 0o700); err != nil {
		t.Fatal(err)
	}
	goodConfig := filepath.Join(root, "good.toml")
	configBody := `[provider]
name = "openrouter"
[providers.openrouter]
base_url = "https://example.com"
api_key_env = "WORKFLOW_TEST_KEY"
models = [{ name = "test/model", context_window_tokens = 128000 }]
[subagents]
store_backend = "sqlite"
store_path = "` + filepath.Join(root, "store.db") + `"
`
	if err := os.WriteFile(goodConfig, []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := executeWorkflowRun("missing", root, goodConfig, nil, false, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing workflow error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".mivia", "workflows", "bad.toml"), []byte("bad = ["), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := executeWorkflowRun("bad", root, goodConfig, nil, false, io.Discard, io.Discard); err == nil {
		t.Fatal("executeWorkflowRun() error = nil for invalid workflow TOML")
	}
}

func TestWorkflowRuntimeSuccessAndPriorChecks(t *testing.T) {
	registry := agents.NewRegistry()
	agent := agents.ResolvedAgent{Name: "worker"}
	if err := registry.Publish(agent); err != nil {
		t.Fatal(err)
	}
	wf := &compiler.CompiledWorkflow{
		Name: "test", Digest: "workflow-digest",
		Steps: []definition.Step{{ID: "one", Kind: "agent", Agent: "worker"}},
	}
	opts := SessionDispatcherOpts{ProviderName: "openrouter", Model: "test/model"}
	prepared, err := prepareWorkflowRuntime(t.TempDir(), "", wf, registry, nil, []byte("definition"), map[string]string{"key": "value"}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Steps["one"].Model != "test/model" {
		t.Fatalf("runtime = %+v", prepared.Steps["one"])
	}
	prior, err := workflowledger.UnmarshalSnapshot(prepared.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	opts.ModelCatalog = []config.ProviderModelGroup{{
		Provider: "openrouter", Selectable: true,
		Models: []config.ModelSpec{{Name: "test/model", ContextWindowTokens: 1000}},
	}}
	if _, err := prepareWorkflowRuntime(t.TempDir(), "", wf, registry, &prior, nil, nil, opts); err != nil {
		t.Fatalf("prepareWorkflowRuntime(prior) error = %v", err)
	}
	pinned := prior.Agents["worker"]
	pinned.ProviderName = "deepseek"
	prior.Agents["worker"] = pinned
	if _, err := prepareWorkflowRuntime(t.TempDir(), "", wf, registry, &prior, nil, nil, opts); err == nil {
		t.Fatal("prepareWorkflowRuntime() accepted a provider that is not in the catalog")
	}
	pinned.ProviderName = "openrouter"
	prior.Agents["worker"] = pinned
	prior.Agents["worker"] = workflowledger.AgentSnapshot{Digest: "changed"}
	if _, _, err := loadWorkflowRuntimes(t.TempDir(), "", wf, registry, &prior); err == nil {
		t.Fatal("loadWorkflowRuntimes() accepted a changed agent")
	}
	if _, err := parseWorkflowInputValue("value", "unsupported"); err == nil {
		t.Fatal("parseWorkflowInputValue() accepted an unsupported type")
	}
	if _, _, _, _, err := loadStepReferences(t.TempDir(), definition.Step{}, nil); err != nil {
		t.Fatalf("loadStepReferences(empty) error = %v", err)
	}
}

func TestSelectWorkflowWorkspaceWriteLifecycle(t *testing.T) {
	root := t.TempDir()
	if _, _, err := selectWorkflowWorkspace(t.Context(), root, "wfr-write", true, nil); err == nil {
		t.Fatal("selectWorkflowWorkspace() error = nil outside a Git repository")
	}
	if err := os.WriteFile(filepath.Join(root, "seed.txt"), []byte("seed"), 0o600); err != nil {
		t.Fatal(err)
	}
	initWorkflowGitRepo(t, root)
	identity, cleanup, err := selectWorkflowWorkspace(t.Context(), root, "wfr-write", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := vcs.Resolve(t.Context(), root, identity.WorktreeName)
	if err != nil || resolved == nil {
		t.Fatalf("created worktree = %+v, %v", resolved, err)
	}
	second, noCleanup, err := selectWorkflowWorkspace(t.Context(), root, "wfr-write", true, nil)
	if err != nil {
		cleanup()
		t.Fatal(err)
	}
	noCleanup()
	if second.WorktreeName != identity.WorktreeName {
		cleanup()
		t.Fatalf("second worktree = %q, want %q", second.WorktreeName, identity.WorktreeName)
	}
	cleanup()
	resolved, err = vcs.Resolve(t.Context(), root, identity.WorktreeName)
	if err != nil || resolved != nil {
		t.Fatalf("removed worktree = %+v, %v", resolved, err)
	}
}
