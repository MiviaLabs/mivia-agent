package cliorchestrate

import (
	"context"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// StoreTestCoordinator stores a coordinator and its repo in the package-level
// maps for the given dispatcher. Returns a cleanup func. Used by cli tests that
// stay in internal/cli but need to set up orchestration state.
func StoreTestCoordinator(d *runtime.Dispatcher, c coordinator.Coordinator, repo ledger.LedgerRepository) func() {
	coordinators.Store(d, c)
	coordinatorRepos.Store(d, repo)
	return func() {
		coordinators.Delete(d)
		coordinatorRepos.Delete(d)
	}
}

// LoadCoordinatorRepo returns the ledger repository registered for d, if any.
func LoadCoordinatorRepo(d *runtime.Dispatcher) (ledger.LedgerRepository, bool) {
	v, ok := coordinatorRepos.Load(d)
	if !ok {
		return nil, false
	}
	repo, ok := v.(ledger.LedgerRepository)
	return repo, ok
}

// LoadCoordinator returns the coordinator registered for d, if any.
func LoadCoordinator(d *runtime.Dispatcher) (coordinator.Coordinator, bool) {
	v, ok := coordinators.Load(d)
	if !ok {
		return nil, false
	}
	c, ok := v.(coordinator.Coordinator)
	return c, ok
}

// StoreTestRunHandle stores an orchestrationHandle for the given runID.
// Returns a cleanup func. Used by cli tests that need to set up run handles.
func StoreTestRunHandle(runID string, c coordinator.Coordinator, h *coordinator.RunHandle, repo ledger.LedgerRepository, d *runtime.Dispatcher, sessionID string) func() {
	record := &orchestrationHandle{
		coord:      c,
		handle:     h,
		repo:       EffectiveOrchestrationRepo(repo),
		dispatcher: d,
		principal:  orchestrationPrincipal{sessionID: sessionID},
		retention:  defaultHandleRetention,
	}
	runHandles.Store(runID, record)
	return func() {
		runHandles.Delete(runID)
	}
}

// CoordinatorForRun returns the coordinator stored for the given runID, if any.
func CoordinatorForRun(runID string) coordinator.Coordinator {
	v, ok := runHandles.Load(runID)
	if !ok {
		return nil
	}
	record, ok := v.(*orchestrationHandle)
	if !ok {
		return nil
	}
	return record.coord
}

// ClearAllCoordinators deletes all entries from the coordinators sync.Map.
// Used by tests to clean up between cases.
func ClearAllCoordinators() {
	coordinators.Range(func(k, _ any) bool {
		coordinators.Delete(k)
		return true
	})
}

// NewDispatchTasksToolZero returns a zero-value dispatch_tasks tool for the
// cli session tool catalog (schema advertising only; no runtime state).
func NewDispatchTasksToolZero() tools.Tool { return &dispatchTasksTool{} }

// NewSpawnAgentToolZero returns a zero-value spawn_agent tool for the cli
// session tool catalog (schema advertising only; no runtime state).
func NewSpawnAgentToolZero() tools.Tool { return &spawnAgentTool{} }

// NewInspectAgentsToolZero returns a zero-value inspect_agents tool for the
// cli session tool catalog (schema advertising only; no runtime state).
func NewInspectAgentsToolZero() tools.Tool { return &inspectAgentTool{} }

// NewJoinRunToolZero returns a zero-value join_run tool for the cli session
// tool catalog (schema advertising only; no runtime state).
func NewJoinRunToolZero() tools.Tool { return &joinRunTool{} }

// NewCancelRunToolZero returns a zero-value cancel_run tool for the cli
// session tool catalog (schema advertising only; no runtime state).
func NewCancelRunToolZero() tools.Tool { return &cancelRunTool{} }

// DispatchTasksToolForTest is the exported type alias for dispatchTasksTool.
// Use for type assertions in cli tests that receive a tools.Tool interface.
type DispatchTasksToolForTest = dispatchTasksTool

// SpawnAgentToolForTest is the exported type alias for spawnAgentTool.
type SpawnAgentToolForTest = spawnAgentTool

// ModelTaskResultForTest is the exported type alias for modelTaskResult.
// Use in cli tests that call ModelTaskResults and need to name the element type.
type ModelTaskResultForTest = modelTaskResult

// NewSpawnAgentToolConfigured builds a spawn_agent tool bound to a dispatcher,
// config, repository, and agent registry. It serves cli tests that construct
// the tool directly.
func NewSpawnAgentToolConfigured(d *runtime.Dispatcher, cfg config.SubagentConfig, repo ledger.LedgerRepository, agentReg *agents.AgentRegistry) *spawnAgentTool {
	return &spawnAgentTool{dispatcher: d, cfg: cfg, repo: repo, agentReg: agentReg}
}

// NewSpawnAgentToolForCatalog builds a spawn_agent tool with only the catalog
// fields set (agent registry, provider, model). It serves schema-level tests.
func NewSpawnAgentToolForCatalog(agentReg *agents.AgentRegistry, providerName, model string) *spawnAgentTool {
	return &spawnAgentTool{agentReg: agentReg, providerName: providerName, model: model}
}

// NewDispatchTasksToolConfigured builds a dispatch_tasks tool bound to a
// dispatcher, config, repository, and agent registry. It serves cli tests
// that construct the tool directly.
func NewDispatchTasksToolConfigured(d *runtime.Dispatcher, cfg config.SubagentConfig, repo ledger.LedgerRepository, agentReg *agents.AgentRegistry) *dispatchTasksTool {
	return &dispatchTasksTool{dispatcher: d, cfg: cfg, repo: repo, agentReg: agentReg}
}

// NewDispatchTasksToolForCatalog builds a dispatch_tasks tool with only the
// catalog fields set (agent registry, provider, model). It serves
// schema-level tests.
func NewDispatchTasksToolForCatalog(agentReg *agents.AgentRegistry, providerName, model string) *dispatchTasksTool {
	return &dispatchTasksTool{agentReg: agentReg, providerName: providerName, model: model}
}

// NewDispatchTasksToolFull builds a dispatch_tasks tool with every field a
// cli test needs to set, including the skill registry.
func NewDispatchTasksToolFull(d *runtime.Dispatcher, cfg config.SubagentConfig, repo ledger.LedgerRepository, agentReg *agents.AgentRegistry, skillReg *skills.Registry) *dispatchTasksTool {
	return &dispatchTasksTool{dispatcher: d, cfg: cfg, repo: repo, agentReg: agentReg, skillReg: skillReg}
}

// OrchestrationHandleForTest is the exported alias of the unexported
// orchestrationHandle record. It serves cli tests that inspect run handles.
type OrchestrationHandleForTest = orchestrationHandle

// RunHandlesForTest exposes the runHandles map for cli tests that seed or
// inspect run-handle records directly.
var RunHandlesForTest = &runHandles

// CoordinatorsForTest exposes the coordinators map for cli tests that seed
// dispatcher-to-coordinator registrations directly.
var CoordinatorsForTest = &coordinators

// CoordinatorReposForTest exposes the coordinatorRepos map for cli tests.
var CoordinatorReposForTest = &coordinatorRepos

// NewDispatchTasksToolForSkillPolicy builds a dispatch_tasks tool with the
// skill registry, agent registry, and config a skill-policy test sets.
func NewDispatchTasksToolForSkillPolicy(skillReg *skills.Registry, agentReg *agents.AgentRegistry, cfg config.SubagentConfig) *dispatchTasksTool {
	return &dispatchTasksTool{skillReg: skillReg, agentReg: agentReg, cfg: cfg}
}

// NewSpawnAgentToolForSkillPolicy builds a spawn_agent tool with the skill
// registry, agent registry, and config a skill-policy test sets.
func NewSpawnAgentToolForSkillPolicy(skillReg *skills.Registry, agentReg *agents.AgentRegistry, cfg config.SubagentConfig) *spawnAgentTool {
	return &spawnAgentTool{skillReg: skillReg, agentReg: agentReg, cfg: cfg}
}

// CoordinatorOfHandle returns the handle record's coordinator. It serves cli
// tests that inspect a parked run's coordinator.
func CoordinatorOfHandle(record *OrchestrationHandleForTest) coordinator.Coordinator {
	return record.coord
}

// PrincipalSessionIDOfHandle returns the handle record's principal session
// id. It serves cli tests that pin resume principal swaps.
func PrincipalSessionIDOfHandle(record *OrchestrationHandleForTest) string {
	return record.principal.sessionID
}

// DispatcherOfHandle returns the handle record's dispatcher.
func DispatcherOfHandle(record *OrchestrationHandleForTest) *runtime.Dispatcher {
	return record.dispatcher
}

// RepoOfHandle returns the handle record's ledger repository.
func RepoOfHandle(record *OrchestrationHandleForTest) ledger.LedgerRepository {
	return record.repo
}

// NewDispatchTasksToolWithCfg builds a dispatch_tasks tool with only the
// config set (encodeResults threshold tests).
func NewDispatchTasksToolWithCfg(cfg config.SubagentConfig) *dispatchTasksTool {
	return &dispatchTasksTool{cfg: cfg}
}

// EncodeResultsForTest exposes the dispatch tool's result encoder.
func (t *dispatchTasksTool) EncodeResultsForTest(tasks []ledger.TaskSnapshot, results []subagents.Result) string {
	return t.encodeResults(tasks, results)
}

// DefaultOrchestrationRepo exposes the fallback in-memory ledger repository
// for tests that inspect the default wiring.
var DefaultOrchestrationRepo = &defaultOrchestrationRepo

// ActiveSessionCallerForTest exposes the active session caller pointer for
// tests that inspect or reset the ambient caller.
func ActiveSessionCallerForTest() *runtime.Caller {
	return activeSessionCaller.Load()
}

// PrincipalForTest builds an orchestration principal value for tests that
// seed handle records.
func PrincipalForTest(sessionID, role string) orchestrationPrincipal {
	return orchestrationPrincipal{sessionID: sessionID, role: role}
}

// StoreHandleForPrincipal seeds a handle record carrying only a principal.
// It serves resume tests that pin principal overwrite behavior.
func StoreHandleForPrincipal(runID, sessionID, role string) {
	runHandles.Store(runID, &orchestrationHandle{principal: orchestrationPrincipal{sessionID: sessionID, role: role}})
}

// OrchestrationHandleAccessibleForTest exposes the principal check used by
// resume enforcement tests.
func OrchestrationHandleAccessibleForTest(ctx context.Context, record *OrchestrationHandleForTest, d *runtime.Dispatcher, repo ledger.LedgerRepository) bool {
	return orchestrationHandleAccessible(ctx, record, d, repo)
}

// NewInspectAgentToolConfigured builds an inspect_agents tool bound to a
// dispatcher.
func NewInspectAgentToolConfigured(d *runtime.Dispatcher) *inspectAgentTool {
	return &inspectAgentTool{dispatcher: d}
}

// NewJoinRunToolConfigured builds a join_run tool bound to a dispatcher.
func NewJoinRunToolConfigured(d *runtime.Dispatcher) *joinRunTool {
	return &joinRunTool{dispatcher: d}
}

// NewCancelRunToolConfigured builds a cancel_run tool bound to a dispatcher.
func NewCancelRunToolConfigured(d *runtime.Dispatcher) *cancelRunTool {
	return &cancelRunTool{dispatcher: d}
}

// BuildTasksForTest exposes the dispatch tool's task builder.
func (t *dispatchTasksTool) BuildTasksForTest(params []dispatchTaskParam, defaultTimeoutSec int) ([]subagents.Task, error) {
	return t.buildTasks(params, defaultTimeoutSec)
}

// BuildSpawnTasksForTest exposes the spawn tool's task builder.
func (t *spawnAgentTool) BuildSpawnTasksForTest(params []spawnTaskParams, caller runtime.Caller) ([]subagents.Task, error) {
	return t.buildSpawnTasks(params, caller)
}

// DispatchTaskParamForTest is the exported alias of the dispatch tool's task
// parameter type.
type DispatchTaskParamForTest = dispatchTaskParam

// SpawnTaskParamsForTest is the exported alias of the spawn tool's task
// parameter type.
type SpawnTaskParamsForTest = spawnTaskParams
