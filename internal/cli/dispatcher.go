package cli

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// NewSessionDispatcher builds a runtime.Dispatcher for agent sessions.
// It registers tool handlers from the tool registry, one-shot and multi-step
// subagent handlers for delegation, optionally wires skills as subagent
// handlers, and adds delegation tools to the tool registry.
//
// If skillReg is non-nil, each skill is registered as a Subagent kind
// handler, making it callable by name from dispatch_tasks.
//
// The onEvent callback is forwarded to the multi-step subagent handler
// so subagent-internal events (tool calls, steps) are visible in the
// parent's TUI.
func NewSessionDispatcher(reg *tools.Registry, comp provider.Completer, model string, cfg config.SubagentConfig, skillReg ...*skills.Registry) (*runtime.Dispatcher, error) {
	repo := defaultOrchestrationRepo
	if cfg.StoreBackend == "sqlite" {
		storePath := cfg.StorePath
		if storePath == "" {
			dir, err := os.UserCacheDir()
			if err != nil {
				dir = os.TempDir()
			}
			cwd, err := os.Getwd()
			if err == nil && cwd != "" {
				h := sha256.Sum256([]byte(cwd))
				storePath = filepath.Join(dir, "mivia", "workspaces", fmt.Sprintf("ws-%x", h[:8]), "orchestration.db")
			} else {
				storePath = filepath.Join(dir, "mivia", "orchestration.db")
			}
		}
		sqlStore, err := storage.OpenSQLite(storePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to open SQLite store %q: %v; falling back to memory backend\n", storePath, err)
		} else {
			storageRepo := ledger.NewStorageLedgerRepository(sqlStore)
			recovered, recErr := storageRepo.Recover(context.Background())
			if recErr != nil {
				fmt.Fprintf(os.Stderr, "warning: orchestration recovery error: %v\n", recErr)
			} else if len(recovered) > 0 {
				for _, r := range recovered {
					if r.WasInterrupted {
						fmt.Fprintf(os.Stderr, "info: recovered interrupted run %s (%s)\n", r.RunID, r.DisplayName)
					}
				}
			}
			repo = storageRepo
		}
	}
	return newSessionDispatcher(reg, comp, model, cfg, repo, skillReg...)
}

// NewSessionDispatcherWithLedger is the durable-repository entry point for
// sessions that must survive dispatcher recreation.
func NewSessionDispatcherWithLedger(reg *tools.Registry, comp provider.Completer, model string, cfg config.SubagentConfig, repo ledger.LedgerRepository, skillReg ...*skills.Registry) (*runtime.Dispatcher, error) {
	if repo == nil {
		return nil, fmt.Errorf("nil orchestration ledger repository")
	}
	return newSessionDispatcher(reg, comp, model, cfg, repo, skillReg...)
}

func newSessionDispatcher(reg *tools.Registry, comp provider.Completer, model string, cfg config.SubagentConfig, repo ledger.LedgerRepository, skillReg ...*skills.Registry) (*runtime.Dispatcher, error) {
	if reg == nil || comp == nil {
		return nil, fmt.Errorf("nil session dispatcher dependency")
	}
	d, err := runtime.NewToolDispatcher(reg, runtime.Policy{
		MaxDepth:  cfg.MaxDepth,
		MaxBudget: cfg.DefaultBudget,
	})
	if err != nil {
		return nil, fmt.Errorf("create tool dispatcher: %w", err)
	}
	if err := registerOneShotHandlers(d, comp, model, cfg); err != nil {
		return nil, err
	}
	if err := registerMultiStepHandler(d, reg, comp, model, cfg); err != nil {
		return nil, err
	}
	var skillsReg *skills.Registry
	if len(skillReg) > 0 {
		skillsReg = skillReg[0]
	}
	if err := registerSkillHandlers(d, skillsReg); err != nil {
		return nil, err
	}
	if err := registerDelegationTools(d, reg, cfg, skillsReg, repo); err != nil {
		return nil, err
	}
	if err := registerOrchestrationTools(d, reg, cfg, repo); err != nil {
		return nil, err
	}
	return d, nil
}

func registerOneShotHandlers(d *runtime.Dispatcher, comp provider.Completer, model string, cfg config.SubagentConfig) error {
	sysPrompt := cfg.SystemPrompt
	if sysPrompt == "" {
		sysPrompt = subagents.DefaultSubagentSystemPrompt
	}
	handler := &subagents.OneShotHandler{
		Completer: comp, Model: model, SystemPrompt: sysPrompt,
	}
	if err := d.Register(runtime.Subagent, "delegate", handler); err != nil {
		return fmt.Errorf("register delegate handler: %w", err)
	}
	if err := d.Register(runtime.Subagent, "oneshot", handler); err != nil {
		return fmt.Errorf("register oneshot handler: %w", err)
	}
	return nil
}

func registerMultiStepHandler(d *runtime.Dispatcher, reg *tools.Registry, comp provider.Completer, model string, cfg config.SubagentConfig) error {
	multiSysPrompt := cfg.SystemPrompt
	if multiSysPrompt == "" {
		multiSysPrompt = subagents.MultiStepSystemPrompt
	}
	// When DefaultTimeout is 0, leave ToolTimeout 0 (handler defaults per-tool
	// to 300s). TotalTimeout stays 0 so req.Timeout from the pool is the bound.
	toolTO := time.Duration(cfg.DefaultTimeout) * time.Second
	totalTO := time.Duration(0)
	if cfg.DefaultTimeout > 0 {
		totalTO = time.Duration(cfg.DefaultTimeout) * time.Second * 3
	}
	h := &subagents.MultiStepHandler{
		Completer: comp, FullRegistry: reg, Dispatcher: d, Model: model,
		SystemPrompt: multiSysPrompt, MaxSteps: cfg.NestedSteps,
		ToolTimeout: toolTO, TotalTimeout: totalTO, MaxTokens: 4096,
		// Forward nested tool/heartbeat events to the session TUI sink
		// registered by startAI via SetSubagentProgress.
		OnEvent: OnEventForMultiStep(emitSubagentProgress),
	}
	if err := d.Register(runtime.Subagent, "multi_step", h); err != nil {
		return fmt.Errorf("register multi-step handler: %w", err)
	}
	return nil
}

func registerSkillHandlers(d *runtime.Dispatcher, skillReg *skills.Registry) error {
	if skillReg == nil {
		return nil
	}
	if err := skillReg.RegisterAll(d); err != nil {
		return fmt.Errorf("register skill tools: %w", err)
	}
	if err := skillReg.RegisterAllAsSubagents(d); err != nil {
		return fmt.Errorf("register skills: %w", err)
	}
	return nil
}

func registerDelegationTools(d *runtime.Dispatcher, reg *tools.Registry, cfg config.SubagentConfig, skillReg *skills.Registry, repo ledger.LedgerRepository) error {
	// Register on both the model-visible registry and the dispatcher snapshot.
	delegate := &delegateTool{dispatcher: d, cfg: cfg, repo: repo}
	dispatchTasks := &dispatchTasksTool{dispatcher: d, cfg: cfg, skillReg: skillReg, repo: repo}
	if err := registerSessionTool(d, reg, delegate); err != nil {
		return err
	}
	return registerSessionTool(d, reg, dispatchTasks)
}

func registerSessionTool(d *runtime.Dispatcher, reg *tools.Registry, tool tools.Tool) error {
	if _, exists := reg.Get(tool.Name()); exists {
		return fmt.Errorf("session tool %q already registered", tool.Name())
	}
	if err := d.RegisterTool(reg, tool); err != nil {
		return fmt.Errorf("register session tool %q: %w", tool.Name(), err)
	}
	reg.Register(tool)
	return nil
}

// OnEventForMultiStep wraps a parent OnEvent callback for forwarding
// subagent events. Tool start/end become SubagentStart/End; heartbeats and
// step progress are forwarded so long multi_step work is not silent.
func OnEventForMultiStep(parentOnEvent func(agent.Event)) func(agent.Event) {
	if parentOnEvent == nil {
		return func(agent.Event) {}
	}
	return func(e agent.Event) {
		switch e.Kind {
		case agent.EventToolStart:
			parentOnEvent(agent.Event{
				Kind: agent.EventSubagentStart, ToolCallID: e.ToolCallID,
				Name: e.Name, Detail: e.Detail, Input: e.Input,
			})
		case agent.EventToolEnd:
			parentOnEvent(agent.Event{
				Kind: agent.EventSubagentEnd, ToolCallID: e.ToolCallID,
				Name: e.Name, Detail: e.Detail, Output: e.Output,
			})
		case agent.EventSubagentHeartbeat:
			parentOnEvent(e)
		case agent.EventStep:
			// Nested agent steps surface as heartbeats in the parent chrome.
			parentOnEvent(agent.Event{
				Kind:   agent.EventSubagentHeartbeat,
				Detail: e.Detail,
			})
		}
	}
}
