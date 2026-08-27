package cliorchestrate

// test_exports_coverage_test.go drives the test-export constructors and
// helpers in test_exports.go directly so the diff-coverage gate sees
// them as covered after the cli split.

import (
	"context"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

func TestNewZeroTools(t *testing.T) {
	// Each Zero constructor returns a non-nil tool instance.
	for name, tool := range map[string]tools.Tool{
		"dispatch_tasks": NewDispatchTasksToolForAdvertising(nil),
		"inspect_agents": NewInspectAgentsToolZero(),
		"join_run":       NewJoinRunToolZero(),
		"cancel_run":     NewCancelRunToolZero(),
	} {
		if tool == nil {
			t.Errorf("%s zero-constructor returned nil", name)
		}
	}
}

func TestNewToolVariants(t *testing.T) {
	// Construct the configured and catalog variants; we pass nil for
	// non-essential dependencies so the constructor bodies exercise
	// their argument-validation branches.
	cfg := config.SubagentConfig{MaxWorkers: 1}
	reg := agents.NewRegistry()
	if got := NewDispatchTasksToolForCatalog(reg, "", ""); got == nil {
		t.Fatal("NewDispatchTasksToolForCatalog returned nil")
	}
	// Skill-policy variants.
	skillReg := skills.NewRegistry()
	if got := NewDispatchTasksToolForSkillPolicy(skillReg, reg, cfg); got == nil {
		t.Fatal("NewDispatchTasksToolForSkillPolicy returned nil")
	}
	// WithCfg variant.
	if got := NewDispatchTasksToolWithCfg(cfg); got == nil {
		t.Fatal("NewDispatchTasksToolWithCfg returned nil")
	}
}

func TestCoordinatorRepoAccessorsAreExported(t *testing.T) {
	// The package-level pointers must be non-nil so callers can
	// traverse the live coordinator / dispatcher maps.
	if RunHandlesForTest == nil {
		t.Fatal("RunHandlesForTest must be non-nil")
	}
	if CoordinatorsForTest == nil {
		t.Fatal("CoordinatorsForTest must be non-nil")
	}
	if CoordinatorReposForTest == nil {
		t.Fatal("CoordinatorReposForTest must be non-nil")
	}
}

func TestAccessorsOnZeroHandle(t *testing.T) {
	// Accessor methods on a zero-value handle must return zero values
	// rather than panic.
	var h OrchestrationHandleForTest
	if got := CoordinatorOfHandle(&h); got != nil {
		t.Errorf("CoordinatorOfHandle(zero) = %v", got)
	}
	if got := PrincipalSessionIDOfHandle(&h); got != "" {
		t.Errorf("PrincipalSessionIDOfHandle(zero) = %q", got)
	}
	if got := DispatcherOfHandle(&h); got != nil {
		t.Errorf("DispatcherOfHandle(zero) = %v", got)
	}
	if got := RepoOfHandle(&h); got != nil {
		t.Errorf("RepoOfHandle(zero) = %v", got)
	}
}

func TestLoadCoordinatorNoMatch(t *testing.T) {
	// LoadCoordinator on a nil dispatcher must return ok=false.
	if _, ok := LoadCoordinator(nil); ok {
		t.Fatal("LoadCoordinator(nil) returned ok=true")
	}
	if _, ok := LoadCoordinatorRepo(nil); ok {
		t.Fatal("LoadCoordinatorRepo(nil) returned ok=true")
	}
}

func TestClearAllCoordinatorsIsNoop(t *testing.T) {
	// Must not panic on empty state.
	ClearAllCoordinators()
}

func TestMoreCliorchestrateTestExports(t *testing.T) {
	// ActiveSessionCallerForTest and PrincipalForTest are simple getters.
	if got := ActiveSessionCallerForTest(); got != nil {
		t.Errorf("ActiveSessionCallerForTest returned non-nil: %v", got)
	}
	p := PrincipalForTest("session-a", "owner")
	if p.sessionID != "session-a" {
		t.Errorf("PrincipalForTest sessionID = %q", p.sessionID)
	}
	// StoreHandleForPrincipal on an empty map must not panic.
	StoreHandleForPrincipal("run-x", "session-a", "owner")
	// OrchestrationHandleAccessibleForTest on a nil record returns false.
	if OrchestrationHandleAccessibleForTest(context.Background(), nil, nil, nil) {
		t.Fatal("OrchestrationHandleAccessibleForTest(nil) must be false")
	}
	// Configured constructors build concrete tools.
	for name, tool := range map[string]func() tools.Tool{
		"inspect_agent": func() tools.Tool { return NewInspectAgentToolConfigured(nil) },
		"join_run":      func() tools.Tool { return NewJoinRunToolConfigured(nil) },
	} {
		if tool() == nil {
			t.Errorf("%s configured-constructor returned nil", name)
		}
	}
}

func TestCoordinatorForRunMissingRun(t *testing.T) {
	if got := CoordinatorForRun("nonexistent"); got != nil {
		t.Errorf("CoordinatorForRun(nonexistent) = %v, want nil", got)
	}
}

func TestStoreTestCoordinatorRoundTrip(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	coord := coordinator.New(repo, nil)
	d := &runtime.Dispatcher{}

	cleanup := StoreTestCoordinator(d, coord, repo)
	t.Cleanup(cleanup)

	gotCoord, ok := LoadCoordinator(d)
	if !ok {
		t.Fatal("LoadCoordinator after StoreTestCoordinator returned ok=false")
	}
	if gotCoord == nil {
		t.Fatal("LoadCoordinator returned nil coordinator")
	}
	gotRepo, ok := LoadCoordinatorRepo(d)
	if !ok {
		t.Fatal("LoadCoordinatorRepo after StoreTestCoordinator returned ok=false")
	}
	if gotRepo == nil {
		t.Fatal("LoadCoordinatorRepo returned nil repository")
	}

	cleanup()
	if _, ok := LoadCoordinator(d); ok {
		t.Error("LoadCoordinator after cleanup returned ok=true")
	}
	if _, ok := LoadCoordinatorRepo(d); ok {
		t.Error("LoadCoordinatorRepo after cleanup returned ok=true")
	}
}

func TestStoreTestRunHandleRoundTrip(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	coord := coordinator.New(repo, nil)

	cleanup := StoreTestRunHandle("run-handle-a", coord, nil, repo, nil, "session-a")
	t.Cleanup(cleanup)
	if got := CoordinatorForRun("run-handle-a"); got == nil {
		t.Fatal("CoordinatorForRun after StoreTestRunHandle returned nil")
	}
	cleanup()
	if got := CoordinatorForRun("run-handle-a"); got != nil {
		t.Errorf("CoordinatorForRun after cleanup = %v, want nil", got)
	}

	// A wrongly typed handle entry must return nil, not panic.
	RunHandlesForTest.Store("run-handle-bogus", "not-a-handle")
	defer RunHandlesForTest.Delete("run-handle-bogus")
	if got := CoordinatorForRun("run-handle-bogus"); got != nil {
		t.Errorf("CoordinatorForRun(wrongly typed entry) = %v, want nil", got)
	}
}

func TestNewDispatchTasksToolFullNilSafe(t *testing.T) {
	if got := NewDispatchTasksToolFull(nil, config.SubagentConfig{}, nil, nil, nil); got == nil {
		t.Fatal("NewDispatchTasksToolFull(nil) returned nil")
	}
}

func TestActiveCoordinatorWithStoredEntry(t *testing.T) {
	// ActiveCoordinator's loop body when a coordinator IS stored: it must
	// stop at the first entry and report ok=true. Store, assert, cleanup.
	repo := ledger.NewMemoryLedgerRepository()
	coord := coordinator.New(repo, nil)
	d := runtime.New(runtime.Policy{})
	cleanup := StoreTestCoordinator(d, coord, repo)
	defer cleanup()
	got, ok := ActiveCoordinator()
	if !ok || got == nil {
		t.Fatalf("ActiveCoordinator() = (%v, %v); want (non-nil, true)", got, ok)
	}
}
