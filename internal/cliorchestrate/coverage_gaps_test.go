package cliorchestrate

// coverage_gaps_test.go drives the specific statement lines the
// diff-coverage gate reported as uncovered in doctor.go, orchestrate.go,
// orchestration_state.go and task_routing.go.

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cliagents "github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
)

func TestRunDoctorRejectsUnknownFlag(t *testing.T) {
	// RunDoctor itself must delegate to RunDoctorWithIO; an unknown flag
	// errors before any environment inspection, so the call is side-effect
	// free apart from the error return.
	if err := RunDoctor([]string{"--no-such-flag"}); err == nil {
		t.Fatal("RunDoctor accepted an unknown flag")
	}
}

func TestWriteDoctorHumanLoadErrorWithCatalog(t *testing.T) {
	// catalogErr == nil makes the load-error screen print the catalog.
	var stdout, stderr bytes.Buffer
	writeDoctorHumanLoadError(&stdout, &stderr, loadCatalogView(t), nil)
	if !strings.Contains(stdout.String(), "mivia doctor") {
		t.Fatalf("stdout = %q, want the doctor header", stdout.String())
	}
	if !strings.Contains(stdout.String(), "agents:") {
		t.Fatalf("stdout = %q, want the agents section", stdout.String())
	}
}

func loadCatalogView(t *testing.T) cliagents.AgentCatalogView {
	t.Helper()
	view, err := cliagents.LoadAgentCatalog(t.TempDir())
	if err != nil {
		t.Fatalf("LoadAgentCatalog: %v", err)
	}
	return view
}

func TestWriteDoctorHumanEnvFileNotLoaded(t *testing.T) {
	var stdout, stderr bytes.Buffer
	res := &config.Resolved{
		ConfigPath:   "/tmp/mivia.toml",
		EnvFilePath:  "/tmp/.env",
		ProviderName: "openai",
		Model:        "m",
		APIKeyEnv:    "OPENAI_API_KEY",
		APIKeySet:    true,
	}
	writeDoctorHuman(&stdout, &stderr, res, loadCatalogView(t), nil, nil)
	if !strings.Contains(stdout.String(), "(not loaded)") {
		t.Fatalf("stdout = %q, want the env-file not-loaded line", stdout.String())
	}
}

func TestRegisterOrchestrationToolsRequiresRegisterSeam(t *testing.T) {
	original := SessionToolRegister
	SessionToolRegister = nil
	t.Cleanup(func() { SessionToolRegister = original })
	if err := RegisterOrchestrationTools(&runtime.Dispatcher{}, nil, config.SubagentConfig{}, nil, nil, nil, "", ""); err == nil {
		t.Fatal("RegisterOrchestrationTools without the register seam must fail")
	}
}

func TestOpenSharedSQLitePaths(t *testing.T) {
	// Non-sqlite backend: a nil no-op pair.
	store, err := OpenSharedSQLite(config.SubagentConfig{StoreBackend: "memory"}, nil)
	if store != nil || err != nil {
		t.Fatalf("OpenSharedSQLite(memory) = (%v, %v), want (nil, nil)", store, err)
	}
	// sqlite backend with an unwritable path under a regular file: error.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	var warnings bytes.Buffer
	store, err = OpenSharedSQLite(config.SubagentConfig{StoreBackend: "sqlite", StorePath: filepath.Join(blocker, "x.db")}, &warnings)
	if err == nil || store != nil {
		t.Fatalf("OpenSharedSQLite(bad path) = (%v, %v), want (nil, error)", store, err)
	}
	if !strings.Contains(warnings.String(), "failed to open shared SQLite store") {
		t.Fatalf("warnings = %q, want the open-failure warning", warnings.String())
	}
	// Working sqlite backend: close through the owner boundary.
	dir := t.TempDir()
	store, err = OpenSharedSQLite(config.SubagentConfig{StoreBackend: "sqlite", StorePath: filepath.Join(dir, "ok.db")}, nil)
	if err != nil || store == nil {
		t.Fatalf("OpenSharedSQLite(ok) = (%v, %v)", store, err)
	}
	if err := CloseSharedSQLite(store); err != nil {
		t.Fatalf("CloseSharedSQLite: %v", err)
	}
	if err := CloseSharedSQLite(nil); err != nil {
		t.Fatalf("CloseSharedSQLite(nil) = %v", err)
	}
}

func TestActiveCoordinatorFindsFirstRegistered(t *testing.T) {
	// The package-level map is shared with other tests, so only the
	// positive path and the non-coordinator skip are asserted here.
	repo := ledger.NewMemoryLedgerRepository()
	coord := coordinator.New(repo, nil)
	d := &runtime.Dispatcher{}
	cleanup := StoreTestCoordinator(d, coord, repo)
	t.Cleanup(cleanup)
	// Poison the map with a non-coordinator entry first: Range must skip it.
	coordinators.Store(&runtime.Dispatcher{}, "not-a-coordinator")
	defer coordinators.Delete(&runtime.Dispatcher{})
	got, ok := ActiveCoordinator()
	if !ok || got == nil {
		t.Fatal("ActiveCoordinator must return the stored coordinator")
	}
}

func TestResolveTaskRouteSkillBranches(t *testing.T) {
	reg := testAgentRegistry(t, "coder")
	// A skill without a registry is refused.
	if _, err := ResolveTaskRoute(reg, nil, "coder", "review"); err == nil || !strings.Contains(err.Error(), "may not invoke skill") {
		t.Fatalf("ResolveTaskRoute(no skill registry) error = %v", err)
	}
	// A skill the registry does not know is refused.
	skillReg := skills.NewRegistry()
	if _, err := ResolveTaskRoute(reg, skillReg, "coder", "nope"); err == nil || !strings.Contains(err.Error(), "unknown skill") {
		t.Fatalf("ResolveTaskRoute(unknown skill) error = %v", err)
	}
}

func TestDispatchRunLevelErrorWithoutResults(t *testing.T) {
	// A dead caller context fails RunThroughCoordinator before any run
	// exists: the tool must still answer with a JSON envelope, never a Go
	// error (the dispatcher would strip the body).
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tool := NewDispatchTasksToolForCatalog(testAgentRegistry(t, "x"), "", "")
	out, err := tool.Execute(ctx, []byte(`{"tasks":[{"id":"a","agent":"x","prompt":"p"}]}`))
	if err != nil {
		t.Fatalf("Execute returned a Go error: %v", err)
	}
	if !strings.Contains(out, `"status"`) {
		t.Fatalf("payload = %q, want a status envelope", out)
	}
}
