package cli

import (
	"fmt"
	"os"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/composition"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/hooks"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/memory"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
	"github.com/MiviaLabs/mivia-agent/internal/remainder"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// SessionDispatcherOpts carries every input the session dispatcher needs.
// Repo and Budget are optional; their absence selects the legacy defaults
// (open a SQLite store from Config, no live budget provider).
type SessionDispatcherOpts struct {
	// Registry is the advertised surface: what the root model is shown and what
	// the root loop may invoke. Under a deferred tool tier this is only the core
	// block plus whatever has been admitted.
	Registry *tools.Registry
	// AuthorityRegistry is the root-scoped FULL authorized tool set, deferred
	// tier included. Delegation authority is not an advertising decision: a
	// routed agent, a skill and a nested multi-step loop are scoped from this,
	// so narrowing what the root model sees never narrows what it may delegate.
	// Nil defaults to Registry, which is the correct answer whenever nothing is
	// deferred.
	AuthorityRegistry *tools.Registry
	Completer         provider.Completer
	Model             string
	ProviderName      string
	// AllowWorkspaceAgentProviders is the user-owned opt-in for static workflow
	// panel provider routing.
	AllowWorkspaceAgentProviders bool
	ModelGeneration              uint64
	// ModelGenerationFunc is evaluated when a routed task starts. Candidate
	// dispatchers are built before a binding is published, so a fixed
	// generation here can be stale after a concurrent switch.
	ModelGenerationFunc func() uint64
	ModelCatalog        []config.ProviderModelGroup
	// CompleterFactory builds a completer bound to one provider. It is required
	// before a routed agent may execute on a provider other than the session's;
	// when it is absent such an agent fails closed rather than silently
	// downgrading onto the session completer. Completers are provider-scoped
	// (the model travels per request), so the model argument is advisory.
	CompleterFactory func(providerName, model string) (provider.Completer, error)
	Config           config.SubagentConfig
	MCP              config.MCPConfig
	// EnsureMCPTools lazily adds wrappers for an authorized routed agent.
	// The session owns the manager and registry that this callback uses.
	EnsureMCPTools     func([]string) error
	ToolResultCapBytes int
	// BatchResultBudgetBytes is the [tools] batch_result_budget_bytes knob,
	// applied to every nested sub-agent loop the same way it applies to the
	// session loop. 0 = off.
	BatchResultBudgetBytes int
	// RefOnlyTools is the [tools] ref_only_tools knob for every nested sub-agent loop; empty = off.
	RefOnlyTools []string
	// WorkspaceRoot is the directory lifecycle hooks execute in. Empty means
	// no hooks are wired, which is what every non-chat caller wants.
	WorkspaceRoot string

	// Memory is the session's memory store (plan 77, E2), the same instance
	// agentSessionState.Memory holds - not a second Open. Nil for every
	// non-chat caller (workflow/background paths); coreMemoryBlockForState
	// already treats a nil store as "", so subagent prompt composition
	// degrades safely with no caller-side nil check required.
	Memory memory.Store
	// MemoryConfig is the resolved [memory] section read alongside Memory.
	MemoryConfig config.MemoryConfig

	// Repo, if set, is used as-is and its lifetime is caller-owned.
	// If nil, the constructor opens a store from Config (with the
	// memory-backend fallback) and owns its Close via dispatcher.OnClose.
	Repo ledger.LedgerRepository

	// MaxContextTokens / MaxTokens configure the nested subagent handlers.
	// Zero values mean "unset" (handler defaults apply).
	MaxContextTokens int
	MaxTokens        *int
	// WorkLimits are session limits. Each task combines these limits with its
	// agent, model, task, and parent-panel limits.
	WorkLimits runtime.WorkLimits

	// Budget, if non-nil, is the live session budget provider read by nested
	// handlers when invoked (so /budget applies without rebuilding).
	Budget func() int

	// Reasoning, if non-nil, is the live session dial read by nested handlers
	// when invoked (so /effort applies without rebuilding). It supersedes the
	// dial resolved from ModelCatalog for every path that follows the session.
	Reasoning func() reasoning.Setting

	// SharedSQLite is a caller-owned SQLite pointer. When supplied, the ledger
	// adapter borrows it and the dispatcher never closes it.
	SharedSQLite *storage.SQLite

	// ContextPreparationManager is a preparation-only capability for nested
	// loops. The dispatcher never receives a checkpoint publisher or store.
	ContextPreparationManager contextmgr.PreparationManager
	ContextPreparationInput   contextmgr.PrepareInput

	// SkillReg, if non-nil, registers each skill as a Subagent handler.
	SkillReg *skills.Registry
	// WorkflowSkillSnapshots pins workflow skill content for every workflow
	// invocation. A nil map means this is not a workflow dispatcher.
	WorkflowSkillSnapshots map[string]workflowledger.RefSnapshot

	// SkillScope is the immutable per-instance skill policy for the selected
	// root agent (plan 06). Zero value allows all skills (no agent selected).
	SkillScope agentSkillScope

	// AgentRegistry is the caller-authorized immutable catalogue whose names
	// are the only task routing targets.
	AgentRegistry *agents.AgentRegistry

	// DeferredTools is this agent binding's frozen deferred set (plan
	// tools/05). Non-empty registers load_tools as a privileged session tool;
	// empty leaves the surface byte-identical to a build without the feature.
	DeferredTools []tools.TierCandidate
	// Session is the session whose tool surface load_tools stages against.
	// Required whenever DeferredTools is non-empty.
	Session *chat.Session

	// RemainderSpool is the live spool of an EXISTING session whose surface is
	// being rebuilt. Visibility grants for truncated output live in the spool
	// instance while the bytes live in a shared store, so minting a new spool
	// for a republished surface would turn every earlier ref into "denied" for
	// the session that produced it. Nil mints one, which is what a genuinely
	// new session wants.
	RemainderSpool *remainder.Spool

	// Sink, when set, receives one runtime.Event per invocation lifecycle
	// step (started, retrying, completed) with bounded audit metadata. Nil
	// disables sink delivery and keeps every other caller unchanged. The sink
	// runs on the invoking goroutine, so it must be cheap and safe for
	// concurrent calls.
	Sink func(runtime.Event)
}

// resultBudgets are the two operator byte knobs every nested loop needs: the
// per-call result cap and the aggregate per-batch budget. They travel together
// because a loop that gets one without the other is bounded in only one of the
// two dimensions that matter.
type resultBudgets struct {
	perCall  int
	perBatch int
	// refOnlyTools names tools whose results are always spooled as refs.
	refOnlyTools []string
}

func (o SessionDispatcherOpts) resultBudgets() resultBudgets {
	return resultBudgets{perCall: o.ToolResultCapBytes, perBatch: o.BatchResultBudgetBytes, refOnlyTools: o.RefOnlyTools}
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
			repo, ownedStore = openDurableLedgerRepo(opts.Config, os.Stderr)
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
	initCoordinator(d, opts.Config, repo)
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
		HooksConfigured:  hookSessionConfigured(),
		HookGroups:       func() []hooks.Group { return currentHookSession().runnable() },
		NoteHookWarnings: func(w []string) { currentHookSession().noteRunWarnings(w) },
	})
	if err != nil {
		return nil, fmt.Errorf("create tool dispatcher: %w", err)
	}
	maxTokens := sessionOutputCeiling(opts)
	authority := opts.authority()
	// Spool is shared by read_output and every nested multi_step loop so a
	// truncation notice minted under one principal resolves for that principal.
	// A rebuilt session surface passes its live spool so the grants it already
	// issued survive the rebuild.
	spool := opts.RemainderSpool
	if spool == nil {
		spool = newRemainderSpool(effectiveOrchestrationRepo(repo))
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
			return registerMultiStepHandler(d, authority, opts.Completer, opts.Model, dial, opts.Config, opts.resultBudgets(), opts.MaxContextTokens, maxTokens, opts.Budget, opts.ContextPreparationManager, opts.ContextPreparationInput, spool)
		},
		func() error { return registerAgentHandlers(d, opts) },
		func() error {
			return registerSkillHandlers(d, authority, opts.Completer, opts.Model, dial, opts.Config, opts.resultBudgets(), opts.MaxContextTokens, maxTokens, opts.Budget, opts.SkillReg, opts.SkillScope, opts.ContextPreparationManager, opts.ContextPreparationInput, spool)
		},
	} {
		if err := register(); err != nil {
			return nil, err
		}
	}
	if err := registerDelegationTools(d, opts.Registry, opts.Config, opts.SkillReg, repo, opts.AgentRegistry, opts.ProviderName, opts.Model); err != nil {
		return nil, err
	}
	if err := registerOrchestrationTools(d, opts.Registry, opts.Config, repo, opts.SkillReg, opts.AgentRegistry, opts.ProviderName, opts.Model); err != nil {
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

// authority resolves the full authorized set nested principals are scoped from.
func (o SessionDispatcherOpts) authority() *tools.Registry {
	if o.AuthorityRegistry == nil {
		return o.Registry
	}
	return o.AuthorityRegistry
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
	return registerSessionTool(d, opts.Registry, &loadToolsTool{session: opts.Session, candidates: opts.DeferredTools})
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

func registerDelegationTools(d *runtime.Dispatcher, reg *tools.Registry, cfg config.SubagentConfig, skillReg *skills.Registry, repo ledger.LedgerRepository, agentReg *agents.AgentRegistry, providerName, model string) error {
	// Register on both the model-visible registry and the dispatcher snapshot.
	delegate := &delegateTool{dispatcher: d, cfg: cfg, repo: repo}
	dispatchTasks := &dispatchTasksTool{dispatcher: d, cfg: cfg, skillReg: skillReg, repo: repo, agentReg: agentReg, providerName: providerName, model: model}
	if err := registerSessionTool(d, reg, delegate); err != nil {
		return err
	}
	return registerSessionTool(d, reg, dispatchTasks)
}

// registerSessionTool is the single entry point for session-control tools.
// Sub-agent registries exclude such tools by the tools.PrivilegedTool marker,
// which is a runtime assertion - so an unmarked control tool would silently
// become callable from a nested agent. Rejecting it here fails at startup
// instead.
func registerSessionTool(d *runtime.Dispatcher, reg *tools.Registry, tool tools.Tool) error {
	if _, privileged := tool.(tools.PrivilegedTool); !privileged {
		return fmt.Errorf("session tool %q must implement tools.PrivilegedTool", tool.Name())
	}
	if _, exists := reg.Get(tool.Name()); exists {
		return fmt.Errorf("session tool %q already registered", tool.Name())
	}
	if err := d.RegisterTool(reg, tool); err != nil {
		return fmt.Errorf("register session tool %q: %w", tool.Name(), err)
	}
	reg.Register(tool)
	return nil
}
