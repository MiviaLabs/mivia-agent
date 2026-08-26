package clichat

import (
	"fmt"
	"os"
	"time"

	cliagents "github.com/MiviaLabs/mivia-agent/internal/cliagents"
	cliorchestrate "github.com/MiviaLabs/mivia-agent/internal/cliorchestrate"
	"github.com/MiviaLabs/mivia-agent/internal/composition"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/hooks"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// resultBudgets are the two operator byte knobs every nested loop needs: the
// per-call result cap and the aggregate per-batch budget. They travel together
// because a loop that gets one without the other is bounded in only one of the
// two dimensions that matter.
type resultBudgets struct {
	perCall  int
	perBatch int
	// refOnlyTools names tools whose results are always spooled as refs.
	refOnlyTools []string
	// toolRunTimeout is the [tools] tool_run_timeout_seconds knob: the SDK
	// registry-wide run backstop for tools with no declared
	// Capability.Timeout. It travels with the budgets because it bounds the
	// same nested loops. <= 0 = no registry-wide cap.
	toolRunTimeout time.Duration
}

// sessionResultBudgets extracts the result budget values from opts.
// Replaces the former (o SessionDispatcherOpts) resultBudgets() method,
// which cannot be defined here since SessionDispatcherOpts is in cliagents.
func sessionResultBudgets(opts SessionDispatcherOpts) resultBudgets {
	return resultBudgets{perCall: opts.ToolResultCapBytes, perBatch: opts.BatchResultBudgetBytes, refOnlyTools: opts.RefOnlyTools, toolRunTimeout: opts.ToolRunTimeout}
}

// selectableModel wraps cliagents.SelectableModel for local use.
func selectableModel(catalog []config.ProviderModelGroup, providerName, model string) (config.ModelSpec, bool) {
	return cliagents.SelectableModel(catalog, providerName, model)
}

// NewSessionDispatcher builds a runtime.Dispatcher for agent sessions from a
// single options struct. This is the only public constructor.
//
// It registers tool handlers from the tool registry, one-shot and multi-step
// subagent handlers for delegation, optionally wires skills as subagent
// handlers, and adds delegation tools to the tool registry.
//
// ToolResultCapBytes is the [tools] max_tool_result_bytes ceiling applied to
// every nested sub-agent loop (multi_step and skill handlers); 0 = uncapped.
func NewSessionDispatcher(opts SessionDispatcherOpts) (*runtime.Dispatcher, error) {
	repo := opts.Repo
	var ownedStore *ledger.StorageLedgerRepository
	if repo == nil {
		if opts.SharedSQLite != nil {
			repo = ledger.NewBorrowedStorageLedgerRepository(opts.SharedSQLite)
		} else {
			repo, ownedStore = cliorchestrate.OpenDurableLedgerRepo(opts.Config, os.Stderr)
		}
	}
	d, err := newSessionDispatcherCore(opts, repo)
	if err != nil {
		if ownedStore != nil {
			_ = ownedStore.Close()
		}
		return nil, err
	}
	if ownedStore != nil {
		d.OnClose(func() { _ = ownedStore.Close() })
	}
	cliorchestrate.InitCoordinator(d, opts.Config, repo)
	return d, nil
}

// newSessionDispatcherMinimal is a test-only convenience for the common
// no-budget, no-repo, no-maxTokens case. Production must use NewSessionDispatcher
// with an explicit SessionDispatcherOpts.
func newSessionDispatcherMinimal(reg *tools.Registry, comp provider.Completer, model string, cfg config.SubagentConfig, toolResultCapBytes int, skillReg ...*skills.Registry) (*runtime.Dispatcher, error) {
	var skillsReg *skills.Registry
	if len(skillReg) > 0 {
		skillsReg = skillReg[0]
	}
	return NewSessionDispatcher(SessionDispatcherOpts{
		Registry:           reg,
		Completer:          comp,
		Model:              model,
		Config:             cfg,
		ToolResultCapBytes: toolResultCapBytes,
		SkillReg:           skillsReg,
	})
}

// newSessionDispatcherCore registers handlers on a dispatcher for the given
// options and repository. The caller owns repo lifetime and initCoordinator.
func newSessionDispatcherCore(opts SessionDispatcherOpts, repo ledger.LedgerRepository) (*runtime.Dispatcher, error) {
	if opts.Registry == nil || opts.Completer == nil {
		return nil, fmt.Errorf("nil session dispatcher dependency")
	}
	// Hook wiring lives on Policy, not on the Dispatcher: Policy is copied to
	// derived dispatchers, so a scoped subagent inherits the gate. A
	// PreToolUse gate a subagent escapes is not a gate.
	d, err := composition.BuildDispatcher(composition.DispatcherInput{
		Registry:         opts.Registry,
		MaxDepth:         opts.Config.MaxDepth,
		MaxBudget:        opts.Config.DefaultBudget,
		Sink:             opts.Sink,
		WorkspaceRoot:    opts.WorkspaceRoot,
		HooksConfigured:  HookSessionConfiguredFunc(),
		HookGroups:       func() []hooks.Group { return CurrentHookSessionFunc().RunnableGroups() },
		NoteHookWarnings: func(w []string) { CurrentHookSessionFunc().NoteRunWarnings(w) },
	})
	if err != nil {
		return nil, fmt.Errorf("create tool dispatcher: %w", err)
	}
	maxTokens := sessionOutputCeiling(opts)
	authority := opts.Authority()
	// Spool is shared by read_output and every nested multi_step loop so a
	// truncation notice minted under one principal resolves for that principal.
	// A rebuilt session surface passes its live spool so the grants it already
	// issued survive the rebuild.
	spool := opts.RemainderSpool
	if spool == nil {
		spool = newRemainderSpool(cliorchestrate.EffectiveOrchestrationRepo(repo))
	}
	dial := sessionDialFor(opts)
	// One loop rather than four copies of the same error branch: handler names
	// share a namespace, so a collision anywhere must abort construction the
	// same way, and a fifth registration cannot forget to check.
	for _, register := range []func() error{
		func() error {
			return registerOneShotHandlers(d, opts.Completer, opts.Model, dial, opts.Config, opts.MaxContextTokens, maxTokens, opts.Budget)
		},
		func() error {
			return registerMultiStepHandler(d, authority, opts.Completer, opts.Model, dial, opts.Config, sessionResultBudgets(opts), opts.MaxContextTokens, maxTokens, opts.Budget, opts.ContextPreparationManager, opts.ContextPreparationInput, spool)
		},
		func() error { return registerAgentHandlers(d, opts) },
		func() error {
			return registerSkillHandlers(d, authority, opts.Completer, opts.Model, dial, opts.Config, sessionResultBudgets(opts), opts.MaxContextTokens, maxTokens, opts.Budget, opts.SkillReg, opts.SkillScope, opts.ContextPreparationManager, opts.ContextPreparationInput, spool)
		},
	} {
		if err := register(); err != nil {
			return nil, err
		}
	}
	if err := cliorchestrate.RegisterOrchestrationTools(d, opts.Registry, opts.Config, repo, opts.SkillReg, opts.AgentRegistry, opts.ProviderName, opts.Model); err != nil {
		return nil, err
	}
	if err := registerMessagingTools(d, opts.Registry, opts.Config, repo, opts.AgentRegistry); err != nil {
		return nil, err
	}
	if _, err := registerLedgerTools(d, opts.Registry, repo, opts.ToolResultCapBytes, spool); err != nil {
		return nil, err
	}
	if err := registerLoadToolsTool(d, opts); err != nil {
		return nil, err
	}
	adoptSessionTools(authority, opts.Registry)
	return d, nil
}

// adoptSessionTools copies the tools this constructor registered onto the
// advertised surface into the authority registry. Handlers hold the authority
// registry by pointer, so this is what keeps a delegated principal's view of
// dispatcher-owned tools (read_output and friends) identical to the behaviour
// when authority and advertised are the same object.
func adoptSessionTools(authority, advertised *tools.Registry) {
	if authority == nil || advertised == nil || authority == advertised {
		return
	}
	for _, tool := range advertised.List() {
		if _, exists := authority.Get(tool.Name()); !exists {
			authority.Register(tool)
		}
	}
}

// registerLoadToolsTool registers the deferred-tool discovery surface. It is
// registered last so it lands after the core block and the admitted tail, and
// only when this binding actually defers something: with nothing deferred there
// is nothing to discover and the tool would be dead schema mass.
func registerLoadToolsTool(d *runtime.Dispatcher, opts SessionDispatcherOpts) error {
	if len(opts.DeferredTools) == 0 {
		return nil
	}
	if opts.Session == nil {
		return fmt.Errorf("deferred tools configured without a session to stage against")
	}
	return cliagents.RegisterSessionTool(d, opts.Registry, cliagents.NewLoadToolsTool(opts.Session, opts.DeferredTools))
}

func sessionOutputCeiling(opts SessionDispatcherOpts) *int {
	profile, ok := selectableModel(opts.ModelCatalog, opts.ProviderName, opts.Model)
	if !ok {
		return opts.MaxTokens
	}
	return config.EffectiveOutputTokens(profile, opts.MaxTokens)
}

// sessionReasoning is the dial configured for the session's own model. Nested
// handlers that follow the session must send the same fields the session does,
// or delegating a task would silently change how hard the model thinks. A
// model outside the catalog yields the zero setting, which sends nothing.
func sessionReasoning(opts SessionDispatcherOpts) reasoning.Setting {
	profile, ok := selectableModel(opts.ModelCatalog, opts.ProviderName, opts.Model)
	if !ok {
		return reasoning.Setting{}
	}
	return config.ModelReasoning(profile)
}

// sessionDial is the dial nested handlers follow, in both forms: the value
// resolved from the catalog at construction, and the live session accessor
// that folds in a /effort override. Handlers prefer the accessor and fall back
// to the value, so a caller with no session (tests, embedders) still sends the
// model's configured depth.
type sessionDial struct {
	static reasoning.Setting
	live   func() reasoning.Setting
}

func sessionDialFor(opts SessionDispatcherOpts) sessionDial {
	return sessionDial{static: sessionReasoning(opts), live: opts.Reasoning}
}
