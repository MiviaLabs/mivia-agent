package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	workflowruntime "github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/vcs"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	workflowspace "github.com/MiviaLabs/mivia-agent/internal/workflows/workspace"
)

type workflowTestDispatcher struct{}

func (workflowTestDispatcher) Close() {}

type workflowSnapshotFailureRepository struct {
	workflowledger.Repository
	err error
}

func (r workflowSnapshotFailureRepository) GetRunSnapshot(context.Context, string) ([]byte, error) {
	return nil, r.err
}

type workflowRunFailureRepository struct {
	workflowledger.Repository
	run workflowledger.RunSnapshot
}

func (r workflowRunFailureRepository) GetRun(context.Context, string) (workflowledger.RunSnapshot, error) {
	return r.run, nil
}

func TestExecuteWorkflowResumeInjectedFailures(t *testing.T) {
	root, configPath, repo, run := newResumeFailureFixture(t)
	sentinel := errors.New("injected resume failure")
	originalOpen := workflowResumeOpenStore
	originalHooks := workflowResumeInstallHooks
	originalBuild := workflowResumeBuild
	originalAdmission := workflowResumeSetAdmission
	originalForce := workflowResumeSetForce
	originalRun := workflowResumeRun
	t.Cleanup(func() {
		workflowResumeOpenStore = originalOpen
		workflowResumeInstallHooks = originalHooks
		workflowResumeBuild = originalBuild
		workflowResumeSetAdmission = originalAdmission
		workflowResumeSetForce = originalForce
		workflowResumeRun = originalRun
	})
	reset := func(selected workflowledger.Repository) {
		workflowResumeOpenStore = func(string, config.SubagentConfig) (*storage.SQLite, workflowledger.Repository, func(), error) {
			return nil, selected, func() {}, nil
		}
		workflowResumeInstallHooks = func(string, bool, bool) (func(), error) { return func() {}, nil }
		workflowResumeBuild = func(string, *config.Resolved, *storage.SQLite, workflowledger.Repository, *compiler.CompiledWorkflow, string, map[string]any, map[string]string, []byte, string, *workflowledger.Snapshot, *workflowledger.RunSnapshot) (workflowControllerBuild, error) {
			return workflowControllerBuild{
				Controller: &controller.LinearController{Holder: "resume-test"},
				Dispatcher: workflowTestDispatcher{},
			}, nil
		}
		workflowResumeSetAdmission = func(workflowControllerBuild) error { return nil }
		workflowResumeSetForce = func(workflowControllerBuild) error { return nil }
		workflowResumeRun = func(context.Context, workflowControllerBuild) (workflowledger.RunSnapshot, error) {
			return workflowledger.RunSnapshot{Status: workflowledger.RunStatusSucceeded}, nil
		}
	}
	runResumeReadFailureTests(t, root, configPath, repo, run, sentinel, reset)
	runResumeExecutionFailureTests(t, root, configPath, repo, run, sentinel, reset)
}

func runResumeReadFailureTests(t *testing.T, root, configPath string, repo workflowledger.Repository, run workflowledger.RunSnapshot, sentinel error, reset func(workflowledger.Repository)) {
	t.Helper()
	t.Run("default root and store", func(t *testing.T) {
		reset(repo)
		workflowResumeOpenStore = func(string, config.SubagentConfig) (*storage.SQLite, workflowledger.Repository, func(), error) {
			return nil, nil, func() {}, sentinel
		}
		err := executeWorkflowResume(run.RunID, "", configPath, true, false, io.Discard, io.Discard)
		if !errors.Is(err, sentinel) {
			t.Fatalf("store error = %v", err)
		}
	})
	t.Run("snapshot read", func(t *testing.T) {
		reset(workflowSnapshotFailureRepository{Repository: repo, err: sentinel})
		err := executeWorkflowResume(run.RunID, root, configPath, true, false, io.Discard, io.Discard)
		if !errors.Is(err, sentinel) {
			t.Fatalf("snapshot error = %v", err)
		}
	})
	t.Run("snapshot validation", func(t *testing.T) {
		bad := run
		bad.SnapshotDigest = "bad"
		reset(workflowRunFailureRepository{Repository: repo, run: bad})
		if err := executeWorkflowResume(run.RunID, root, configPath, true, false, io.Discard, io.Discard); err == nil {
			t.Fatal("executeWorkflowResume() accepted a bad snapshot digest")
		}
	})
}

func runResumeExecutionFailureTests(t *testing.T, root, configPath string, repo workflowledger.Repository, run workflowledger.RunSnapshot, sentinel error, reset func(workflowledger.Repository)) {
	t.Helper()
	t.Run("hooks", func(t *testing.T) {
		reset(repo)
		workflowResumeInstallHooks = func(string, bool, bool) (func(), error) { return nil, sentinel }
		if err := executeWorkflowResume(run.RunID, root, configPath, true, false, io.Discard, io.Discard); !errors.Is(err, sentinel) {
			t.Fatalf("hook error = %v", err)
		}
	})
	t.Run("build", func(t *testing.T) {
		reset(repo)
		workflowResumeBuild = func(string, *config.Resolved, *storage.SQLite, workflowledger.Repository, *compiler.CompiledWorkflow, string, map[string]any, map[string]string, []byte, string, *workflowledger.Snapshot, *workflowledger.RunSnapshot) (workflowControllerBuild, error) {
			return workflowControllerBuild{}, sentinel
		}
		if err := executeWorkflowResume(run.RunID, root, configPath, true, false, io.Discard, io.Discard); !errors.Is(err, sentinel) {
			t.Fatalf("build error = %v", err)
		}
	})
	t.Run("admission", func(t *testing.T) {
		reset(repo)
		workflowResumeSetAdmission = func(workflowControllerBuild) error { return sentinel }
		if err := executeWorkflowResume(run.RunID, root, configPath, true, false, io.Discard, io.Discard); !errors.Is(err, sentinel) {
			t.Fatalf("admission error = %v", err)
		}
	})
	t.Run("force", func(t *testing.T) {
		reset(repo)
		workflowResumeSetForce = func(workflowControllerBuild) error { return sentinel }
		if err := executeWorkflowResume(run.RunID, root, configPath, true, false, io.Discard, io.Discard); !errors.Is(err, sentinel) {
			t.Fatalf("force error = %v", err)
		}
	})
	t.Run("claim", func(t *testing.T) {
		failing := &workflowFailureRepository{Repository: repo, takeoverErr: sentinel}
		reset(failing)
		err := executeWorkflowResume(run.RunID, root, configPath, true, false, io.Discard, io.Discard)
		if !errors.Is(err, sentinel) || !strings.Contains(err.Error(), "claim workflow resume handoff") {
			t.Fatalf("claim error = %v", err)
		}
	})
	t.Run("run releases handoff claim", func(t *testing.T) {
		reset(repo)
		workflowResumeRun = func(context.Context, workflowControllerBuild) (workflowledger.RunSnapshot, error) {
			return workflowledger.RunSnapshot{}, sentinel
		}
		err := executeWorkflowResume(run.RunID, root, configPath, true, false, io.Discard, io.Discard)
		if !errors.Is(err, sentinel) {
			t.Fatalf("run error = %v", err)
		}
		if err := repo.ClaimRun(t.Context(), run.RunID, "next-resumer"); err != nil {
			t.Fatalf("claim after failed resume = %v", err)
		}
		if err := repo.ReleaseRun(t.Context(), run.RunID, "next-resumer"); err != nil {
			t.Fatalf("release after failed resume = %v", err)
		}
	})
}

func newResumeFailureFixture(t *testing.T) (string, string, *workflowledger.StorageRepository, workflowledger.RunSnapshot) {
	t.Helper()
	root := t.TempDir()
	storePath := filepath.Join(root, "unused.db")
	writeWorkflowRunFixture(t, root, "https://example.com", storePath)
	configPath := filepath.Join(root, "config.toml")
	raw, err := os.ReadFile(filepath.Join(root, ".mivia", "workflows", "two-step.toml"))
	if err != nil {
		t.Fatal(err)
	}
	wf, _, err := definition.ParseWorkflowTOML(raw, "two-step.toml")
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile(&wf)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := workflowledger.Snapshot{
		SchemaVersion:  workflowledger.SnapshotSchemaVersion,
		DefinitionTOML: raw, DefinitionDigest: compiled.Digest,
		Inputs: map[string]string{"task": "test"},
		Agents: map[string]workflowledger.AgentSnapshot{
			"one": {Digest: "one"}, "two": {Digest: "two"},
		},
		Schemas: map[string]workflowledger.RefSnapshot{}, Templates: map[string]workflowledger.RefSnapshot{},
	}
	for _, step := range compiled.Steps {
		for ref, target := range map[string]map[string]workflowledger.RefSnapshot{
			step.Template: snapshot.Templates, step.OutputSchema: snapshot.Schemas,
		} {
			if ref == "" {
				continue
			}
			data, readErr := os.ReadFile(filepath.Join(root, ".mivia", "workflows", ref))
			if readErr != nil {
				t.Fatal(readErr)
			}
			target[ref] = workflowledger.RefSnapshot{Digest: digestBytes(data), Bytes: data}
		}
	}
	rawSnapshot, err := workflowledger.MarshalSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	run := workflowledger.RunSnapshot{
		RunID: "wfr-resume-failures", WorkflowName: compiled.Name, WorkflowDigest: compiled.Digest,
		SnapshotDigest: workflowledger.SnapshotDigest(rawSnapshot), InputDigest: workflowledger.InputDigest(snapshot.Inputs),
		Status: workflowledger.RunStatusPending, ActiveStepID: compiled.InitialStep,
	}
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.CreateRun(t.Context(), run, rawSnapshot); err != nil {
		t.Fatal(err)
	}
	return root, configPath, repo, run
}

func TestValidateWorkflowResumeSnapshotRemainingErrors(t *testing.T) {
	root, _, repo, run := newResumeFailureFixture(t)
	raw, err := repo.GetRunSnapshot(t.Context(), run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*workflowledger.RunSnapshot, *workflowledger.Snapshot)
	}{
		{"input digest", func(run *workflowledger.RunSnapshot, _ *workflowledger.Snapshot) { run.InputDigest = "bad" }},
		{"definition digest", func(_ *workflowledger.RunSnapshot, snap *workflowledger.Snapshot) { snap.DefinitionDigest = "bad" }},
		{"references", func(_ *workflowledger.RunSnapshot, snap *workflowledger.Snapshot) { snap.Agents = nil }},
		{"unknown input", func(_ *workflowledger.RunSnapshot, snap *workflowledger.Snapshot) { snap.Inputs["unknown"] = "x" }},
		{"input parse", func(_ *workflowledger.RunSnapshot, snap *workflowledger.Snapshot) { snap.Inputs["task"] = "x" }},
	}
	_ = root
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot, unmarshalErr := workflowledger.UnmarshalSnapshot(raw)
			if unmarshalErr != nil {
				t.Fatal(unmarshalErr)
			}
			caseRun := run
			if test.name == "input parse" {
				snapshot.DefinitionTOML = []byte(strings.ReplaceAll(string(snapshot.DefinitionTOML), `type = "string"`, `type = "integer"`))
				wf, _, parseErr := definition.ParseWorkflowTOML(snapshot.DefinitionTOML, "two-step.toml")
				if parseErr != nil {
					t.Fatal(parseErr)
				}
				compiled, compileErr := compiler.Compile(&wf)
				if compileErr != nil {
					t.Fatal(compileErr)
				}
				snapshot.DefinitionDigest = compiled.Digest
				caseRun.WorkflowDigest = compiled.Digest
			}
			test.mutate(&caseRun, &snapshot)
			caseRaw, marshalErr := workflowledger.MarshalSnapshot(snapshot)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			caseRun.SnapshotDigest = workflowledger.SnapshotDigest(caseRaw)
			if test.name != "input digest" {
				caseRun.InputDigest = workflowledger.InputDigest(snapshot.Inputs)
			}
			if _, _, _, err := validateWorkflowResumeSnapshot(caseRun, caseRaw); err == nil {
				t.Fatalf("validateWorkflowResumeSnapshot() accepted %s", test.name)
			}
		})
	}
	compileRaw := []byte("version = 1\nname = \"bad\"\ninitial_step = \"one\"\n[[steps]]\nid = \"one\"\nkind = \"agent\"\nagent = \"worker\"\n")
	compileSnapshot := workflowledger.Snapshot{SchemaVersion: 1, DefinitionTOML: compileRaw, DefinitionDigest: "digest"}
	compileBytes, err := workflowledger.MarshalSnapshot(compileSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	compileRun := workflowledger.RunSnapshot{
		RunID: "wfr-compile", WorkflowName: "bad", SnapshotDigest: workflowledger.SnapshotDigest(compileBytes),
		InputDigest: workflowledger.InputDigest(nil),
	}
	if _, _, _, err := validateWorkflowResumeSnapshot(compileRun, compileBytes); err == nil {
		t.Fatal("validateWorkflowResumeSnapshot() accepted an invalid compiled workflow")
	}
}

func TestWorkflowExecutionLockRemainingErrors(t *testing.T) {
	root := t.TempDir()
	storePath := filepath.Join(root, "store.db")
	sentinel := errors.New("injected execution error")
	originalHooks := workflowExecutionHooks
	workflowExecutionHooks = func(string, bool, bool) (func(), error) { return nil, sentinel }
	if _, err := beginWorkflowExecution(root, storePath, "wfr-hooks"); !errors.Is(err, sentinel) {
		t.Fatalf("hook error = %v", err)
	}
	workflowExecutionHooks = originalHooks
	release, err := acquireWorkflowExecutionLock(storePath, "wfr-hooks")
	if err != nil {
		t.Fatalf("hook failure retained the lock: %v", err)
	}
	release()
	originalAbs := workflowStoreAbs
	workflowStoreAbs = func(string) (string, error) { return "", sentinel }
	if _, _, err := workflowStoreLockIdentity("store.db"); !errors.Is(err, sentinel) {
		t.Fatalf("absolute path error = %v", err)
	}
	workflowStoreAbs = originalAbs
	if err := os.WriteFile(storePath, []byte("store"), 0o600); err != nil {
		t.Fatal(err)
	}
	originalStoreStat := workflowStoreStat
	workflowStoreStat = func(string) (os.FileInfo, error) { return nil, sentinel }
	if _, _, err := workflowStoreLockIdentity(storePath); !errors.Is(err, sentinel) {
		t.Fatalf("store stat error = %v", err)
	}
	workflowStoreStat = originalStoreStat
	t.Cleanup(func() {
		workflowExecutionHooks = originalHooks
		workflowStoreAbs = originalAbs
		workflowStoreStat = originalStoreStat
	})
	originalOpenRoot := workflowExecutionOpenRoot
	workflowExecutionOpenRoot = func(string) (*os.Root, error) { return nil, sentinel }
	if _, err := acquireWorkflowExecutionLock(storePath, "wfr-root"); !errors.Is(err, sentinel) {
		t.Fatalf("open root error = %v", err)
	}
	workflowExecutionOpenRoot = originalOpenRoot
	originalOpenDir := workflowExecutionOpenDir
	workflowExecutionOpenDir = func(*os.Root, string) (*os.Root, error) { return nil, sentinel }
	if _, err := acquireWorkflowExecutionLock(storePath, "wfr-directory"); !errors.Is(err, sentinel) {
		t.Fatalf("open directory error = %v", err)
	}
	workflowExecutionOpenDir = originalOpenDir
	t.Cleanup(func() {
		workflowExecutionOpenRoot = originalOpenRoot
		workflowExecutionOpenDir = originalOpenDir
	})
	symlinkRoot := t.TempDir()
	target := filepath.Join(symlinkRoot, "lock-target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(symlinkRoot, workflowExecutionLockDir)
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := acquireWorkflowExecutionLock(filepath.Join(symlinkRoot, "store.db"), "wfr-link"); err == nil {
		t.Fatal("acquireWorkflowExecutionLock() accepted a symbolic-link directory")
	}
}

func TestSelectWorkflowWorkspaceInjectedFailures(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "seed.txt"), []byte("seed"), 0o600); err != nil {
		t.Fatal(err)
	}
	initWorkflowGitRepo(t, root)
	sentinel := errors.New("injected workspace error")
	originalResolve := workflowVCSResolve
	originalEnsure := workflowWorkspaceEnsure
	t.Cleanup(func() {
		workflowVCSResolve = originalResolve
		workflowWorkspaceEnsure = originalEnsure
	})
	workflowVCSResolve = func(context.Context, string, string) (*vcs.WorktreeInfo, error) { return nil, sentinel }
	if _, _, err := selectWorkflowWorkspace(t.Context(), root, "wfr-resolve", true, nil); !errors.Is(err, sentinel) {
		t.Fatalf("resolve error = %v", err)
	}
	workflowVCSResolve = originalResolve
	workflowWorkspaceEnsure = func(context.Context, string, string, workflowspace.Isolation) (workflowspace.Identity, error) {
		return workflowspace.Identity{}, sentinel
	}
	if _, _, err := selectWorkflowWorkspace(t.Context(), root, "wfr-ensure", true, nil); !errors.Is(err, sentinel) {
		t.Fatalf("ensure error = %v", err)
	}
}

func TestExecuteWorkflowRunCleansFailedAdmission(t *testing.T) {
	root := t.TempDir()
	storePath := filepath.Join(root, "workflow.db")
	writeWorkflowRunFixture(t, root, "https://example.com", storePath)
	sentinel := errors.New("admission failed")
	originalBuild := workflowRunBuild
	originalAdmission := workflowRunSetAdmission
	cleaned := false
	workflowRunBuild = func(string, *config.Resolved, *storage.SQLite, workflowledger.Repository, *compiler.CompiledWorkflow, string, map[string]any, map[string]string, []byte, string, *workflowledger.Snapshot, *workflowledger.RunSnapshot) (workflowControllerBuild, error) {
		return workflowControllerBuild{Dispatcher: workflowTestDispatcher{}, Cleanup: func() { cleaned = true }}, nil
	}
	workflowRunSetAdmission = func(workflowControllerBuild) error { return sentinel }
	t.Cleanup(func() {
		workflowRunBuild = originalBuild
		workflowRunSetAdmission = originalAdmission
	})
	err := executeWorkflowRun("two-step", root, filepath.Join(root, "config.toml"), []string{"task=test"}, false, io.Discard, io.Discard)
	if !errors.Is(err, sentinel) || !cleaned {
		t.Fatalf("executeWorkflowRun() error = %v, cleaned = %v", err, cleaned)
	}
}

func TestBuildWorkflowControllerDependencyFailures(t *testing.T) {
	root, res, store, repo, wf := newWorkflowBuildFixture(t)
	sentinel := errors.New("injected build failure")
	originalSkills := workflowBuildLoadSkills
	originalAgents := workflowBuildLoadAgents
	originalRegistry := workflowBuildRegistry
	originalWorkspace := workflowBuildWorkspace
	originalProvider := workflowBuildProvider
	originalDispatcher := workflowBuildDispatcher
	originalController := workflowBuildController
	reset := func() {
		workflowBuildLoadSkills = originalSkills
		workflowBuildLoadAgents = originalAgents
		workflowBuildRegistry = originalRegistry
		workflowBuildWorkspace = originalWorkspace
		workflowBuildProvider = originalProvider
		workflowBuildDispatcher = originalDispatcher
		workflowBuildController = originalController
	}
	t.Cleanup(reset)
	call := func() error {
		_, err := buildWorkflowController(root, res, store, repo, wf, filepath.Join(root, ".mivia", "workflows"), map[string]any{"task": "test"}, map[string]string{"task": "test"}, []byte("definition"), "wfr-build-failure", nil, nil)
		return err
	}
	runEarlyWorkflowBuildFailureTests(t, sentinel, reset, call)
	runLateWorkflowBuildFailureTests(t, sentinel, reset, call, originalRegistry)
}

func TestBuildWorkflowControllerConfiguresEvidenceInIsolatedWorktree(t *testing.T) {
	root, res, store, repo, wf := newWorkflowBuildFixture(t)
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/workflow-test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	initWorkflowGitRepo(t, root)
	writer := `name = "writer"
description = "workflow test writer"
tools = ["write_file"]
max_turns = 1
`
	if err := os.WriteFile(filepath.Join(root, ".mivia", "agents", "writer.toml"), []byte(writer), 0o600); err != nil {
		t.Fatal(err)
	}
	wf.Steps[0].Agent = "writer"
	wf.Steps = append(wf.Steps,
		definition.Step{ID: "review", Kind: "agent_gate", Agent: "two", Template: "templates/two.md", OutputSchema: "schemas/out.json"},
		definition.Step{ID: "verify", Kind: "evidence_gate", Verifier: "go-test", OutputSchema: "schemas/out.json"},
	)
	built, err := buildWorkflowController(root, res, store, repo, wf, filepath.Join(root, ".mivia", "workflows"), map[string]any{"task": "test"}, map[string]string{"task": "test"}, []byte("definition"), "wfr-evidence-wiring", nil, nil)
	if err != nil {
		t.Fatalf("buildWorkflowController() error = %v", err)
	}
	t.Cleanup(func() {
		built.Dispatcher.Close()
		built.Cleanup()
	})
	if _, err := built.Controller.Verifiers.Lookup("go-test"); err != nil {
		t.Fatalf("controller verifier catalogue = %v", err)
	}
	if built.Controller.WorkDir == "" || built.Controller.WorkDir == root {
		t.Fatalf("controller work directory = %q; want isolated worktree", built.Controller.WorkDir)
	}
}

func runEarlyWorkflowBuildFailureTests(t *testing.T, sentinel error, reset func(), call func() error) {
	t.Helper()
	check := func(t *testing.T) {
		t.Helper()
		if err := call(); !errors.Is(err, sentinel) {
			t.Fatalf("build error = %v", err)
		}
	}
	t.Run("skills", func(t *testing.T) {
		reset()
		workflowBuildLoadSkills = func(string) (*skills.Registry, error) { return nil, sentinel }
		check(t)
	})
	t.Run("agents", func(t *testing.T) {
		reset()
		workflowBuildLoadAgents = func(string, string, *skills.Registry) (agentLoadResult, error) {
			return agentLoadResult{}, sentinel
		}
		check(t)
	})
	t.Run("registry", func(t *testing.T) {
		reset()
		workflowBuildRegistry = func(string, *config.Resolved) (*tools.Registry, error) { return nil, sentinel }
		check(t)
	})
	t.Run("workspace", func(t *testing.T) {
		reset()
		workflowBuildWorkspace = func(context.Context, string, string, bool, *workflowledger.RunSnapshot) (workflowspace.Identity, func(), error) {
			return workflowspace.Identity{}, nil, sentinel
		}
		check(t)
	})
}

func runLateWorkflowBuildFailureTests(t *testing.T, sentinel error, reset func(), call func() error, originalRegistry func(string, *config.Resolved) (*tools.Registry, error)) {
	t.Helper()
	check := func(t *testing.T) {
		t.Helper()
		if err := call(); !errors.Is(err, sentinel) {
			t.Fatalf("build error = %v", err)
		}
	}
	t.Run("worktree registry", func(t *testing.T) {
		reset()
		calls := 0
		workflowBuildRegistry = func(root string, resolved *config.Resolved) (*tools.Registry, error) {
			calls++
			if calls == 2 {
				return nil, sentinel
			}
			return originalRegistry(root, resolved)
		}
		check(t)
	})
	t.Run("provider", func(t *testing.T) {
		reset()
		workflowBuildProvider = func(*config.Resolved) (provider.Completer, error) { return nil, sentinel }
		check(t)
	})
	t.Run("dispatcher", func(t *testing.T) {
		reset()
		workflowBuildDispatcher = func(SessionDispatcherOpts) (*workflowruntime.Dispatcher, error) { return nil, sentinel }
		check(t)
	})
	t.Run("controller", func(t *testing.T) {
		reset()
		workflowBuildController = func(workflowledger.Repository, controller.AgentStepRunner, *compiler.CompiledWorkflow, map[string]controller.StepRuntime, map[string]any, string, []byte) (*controller.LinearController, error) {
			return nil, sentinel
		}
		check(t)
	})
}

func newWorkflowBuildFixture(t *testing.T) (string, *config.Resolved, *storage.SQLite, workflowledger.Repository, *compiler.CompiledWorkflow) {
	t.Helper()
	root := t.TempDir()
	storePath := filepath.Join(root, "workflow.db")
	writeWorkflowRunFixture(t, root, "https://example.com", storePath)
	res, err := config.Load(config.LoadOptions{ConfigPath: filepath.Join(root, "config.toml")})
	if err != nil {
		t.Fatal(err)
	}
	store, repo, closeFn, err := openWorkflowStore(root, res.Subagents)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeFn)
	raw, err := os.ReadFile(filepath.Join(root, ".mivia", "workflows", "two-step.toml"))
	if err != nil {
		t.Fatal(err)
	}
	definitionFile, _, err := definition.ParseWorkflowTOML(raw, "two-step.toml")
	if err != nil {
		t.Fatal(err)
	}
	wf, err := compiler.Compile(&definitionFile)
	if err != nil {
		t.Fatal(err)
	}
	return root, res, store, repo, wf
}
