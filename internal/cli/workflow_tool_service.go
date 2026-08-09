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
	return workflowToolServiceWithBus(root, res, nil)
}

// workflowToolServiceWithBus builds the service like workflowToolService and
// attaches the session event bus provider to its engine. The provider is read
// at controller attach time, so a bus created after wiring is still observed.
// provider may be nil.
func workflowToolServiceWithBus(root string, res *config.Resolved, provider func() *events.Bus) *agenttools.Service {
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
	svc, err := agenttools.NewService(agenttools.ServiceOptions{
		Engine: engine,
		Repo:   repoFactory,
	})
	if err != nil {
		return nil
	}
	return svc
}

// wireWorkflowToolOptions attaches Phase 7 workflow tools to DefaultOptions
// when the workspace has .mivia/workflows/. Reads and mutates share one config
// identity (session ConfigPath or workspace project file). provider supplies
// the session event bus for workflow progress lazily, so a bus created after
// wiring is still observed. nil disables progress publishing.
func wireWorkflowToolOptions(opts *tools.DefaultOptions, root string, res *config.Resolved, provider func() *events.Bus) {
	if opts == nil {
		return
	}
	svc := workflowToolServiceWithBus(root, res, provider)
	if svc == nil {
		return
	}
	opts.WorkflowTools = wrapWorkflowTools(svc)
}
