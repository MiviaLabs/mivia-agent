package cli

import (
	"context"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/agenttools"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// workflowToolSubagentConfig resolves the store config used by workflow tools.
// It matches CLI workflow commands: prefer the session Resolved config, else
// load the workspace config, then apply the workspace store-root default.
func workflowToolSubagentConfig(root string, res *config.Resolved) config.SubagentConfig {
	if res != nil {
		applyWorkflowStoreRoot(res, root)
		return res.Subagents
	}
	configPath := sessionEngineConfigPath(root, nil)
	loaded, err := config.Load(config.LoadOptions{ConfigPath: configPath, WorkspaceRoot: root, AllowMissingConfig: true})
	if err != nil || loaded == nil {
		return config.DefaultSubagentConfig
	}
	applyWorkflowStoreRoot(loaded, root)
	return loaded.Subagents
}

// sessionEngineConfigPath is the config file identity for session workflow
// tools. Prefer the session Resolved.ConfigPath (covers --config / MIVIA_CONFIG)
// so read and mutate paths open the same store. Fall back to the workspace
// project file when no session config is available.
func sessionEngineConfigPath(root string, res *config.Resolved) string {
	if res != nil && strings.TrimSpace(res.ConfigPath) != "" {
		return res.ConfigPath
	}
	return workflowConfigPath(root, "")
}

// workflowToolService builds the in-process workflow tool service for a
// workspace. res carries the session config identity when available; nil
// falls back to the workspace project config. Returns nil when the workspace
// has no .mivia/workflows/ or the service cannot be built.
func workflowToolService(root string, res *config.Resolved) *agenttools.Service {
	return workflowToolServiceWithBus(root, res, nil, false)
}

// workflowToolServiceWithBus builds the service like workflowToolService and
// attaches the session event bus provider to its engine. The provider is read
// at controller attach time, so a bus created after wiring is still observed.
// provider may be nil. quiet (--quiet) suppresses the session-start recovery
// sweep's per-run skip/failure logs, the same way it suppresses the other
// startup notices.
func workflowToolServiceWithBus(root string, res *config.Resolved, provider func() *events.Bus, quiet bool) *agenttools.Service {
	if !agenttools.HasWorkflows(root) {
		return nil
	}
	cfg := workflowToolSubagentConfig(root, res)
	configPath := sessionEngineConfigPath(root, res)
	repoFactory := func(context.Context) (workflowledger.Repository, func(), error) {
		_, repo, closeFn, err := openWorkflowStore(root, cfg)
		return repo, closeFn, err
	}
	engine := newSessionWorkflowEngine(root, configPath)
	engine.SetEventBusProvider(provider)
	// A workflow that declares a [delivery] policy grants publication: the
	// harness must honor it always, without flags or manual overrides. When
	// the harness wires its workflow surface (a non-nil event-bus provider
	// marks production wiring; tests pass nil), recover runs left unfinished
	// by an earlier session (restart or crash): publish delivery_pending runs
	// and resume pending/running/waiting_approval runs whose claim is free or
	// expired. The one-shot sweep covers the moment of wiring; the periodic
	// re-scan then keeps picking up runs whose claim expires mid-session, so
	// a dead run resumes on its own without a restart. The sweep is
	// serialized per run by the execution file lock and fenced by the run
	// claim, so it never races a live executor, and delivery refuses runs
	// without an active policy. The one-shot sweep inherits the session's
	// quiet flag so --quiet also silences its recovery notices.
	if provider != nil {
		go engine.reconcileParkedRuns(context.Background(), quiet)
		go engine.reconcileParkedRunsPeriodic(context.Background())
	}
	// NewService fails only when the repository factory is nil; this caller
	// always provides one, so the error is impossible by construction and the
	// branch would be dead code (diff-coverage gate).
	svc, _ := agenttools.NewService(agenttools.ServiceOptions{
		Engine: engine,
		Repo:   repoFactory,
	})
	return svc
}

// wireWorkflowToolOptions attaches Phase 7 workflow tools to DefaultOptions
// when the workspace has .mivia/workflows/. Reads and mutates share one config
// identity (session ConfigPath or workspace project file). provider supplies
// the session event bus for workflow progress lazily, so a bus created after
// wiring is still observed. nil disables progress publishing.
//
// The parked-delivery sweep (see workflowToolServiceWithBus) already runs when
// provider != nil, so no sweep is launched here. quiet (--quiet) is forwarded
// to that sweep so the session-start recovery notices honor it.
func wireWorkflowToolOptions(opts *tools.DefaultOptions, root string, res *config.Resolved, provider func() *events.Bus, quiet bool) {
	if opts == nil {
		return
	}
	svc := workflowToolServiceWithBus(root, res, provider, quiet)
	if svc == nil {
		return
	}
	opts.WorkflowTools = wrapWorkflowTools(svc)
}
