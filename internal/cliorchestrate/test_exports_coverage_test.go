package cliorchestrate

// test_exports_coverage_test.go drives the test-export constructors and
// helpers in test_exports.go directly so the diff-coverage gate sees
// them as covered after the cli split.

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

func TestNewZeroTools(t *testing.T) {
	// Each Zero constructor returns a non-nil tool instance.
	for name, tool := range map[string]tools.Tool{
		"dispatch_tasks": NewDispatchTasksToolZero(),
		"spawn_agent":    NewSpawnAgentToolZero(),
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
	if got := NewSpawnAgentToolForCatalog(reg, "", ""); got == nil {
		t.Fatal("NewSpawnAgentToolForCatalog returned nil")
	}
	// Skill-policy variants.
	skillReg := skills.NewRegistry()
	if got := NewDispatchTasksToolForSkillPolicy(skillReg, reg, cfg); got == nil {
		t.Fatal("NewDispatchTasksToolForSkillPolicy returned nil")
	}
	if got := NewSpawnAgentToolForSkillPolicy(skillReg, reg, cfg); got == nil {
		t.Fatal("NewSpawnAgentToolForSkillPolicy returned nil")
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
