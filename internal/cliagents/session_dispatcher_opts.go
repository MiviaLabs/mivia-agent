package cliagents

import (
	"context"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/memory"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
	"github.com/MiviaLabs/mivia-agent/internal/remainder"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// SessionDispatcherOpts carries every input the session dispatcher needs.
// Repo and Budget are optional; their absence selects the legacy defaults
// (open a SQLite store from Config, no live budget provider).
// Moved here from internal/cli/dispatcher.go so agent_binding.go and
// other cliagents code can reference the type without importing cli.
// internal/cli/dispatcher.go re-exports it via a type alias.
type SessionDispatcherOpts struct {
	// ToolDenylist is the operator's mandatory_tool_denylist. Session-owned
	// tools are registered after the registry has been scoped, so this is the
	// only point at which the operator's guardrail can refuse one.
	ToolDenylist []string
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
	// Approval supplies the operator's live approval wiring to every nested
	// loop this dispatcher builds: the gate, the policy and the standing
	// cache. Read per invocation, never captured - the gate is installed
	// after the dispatcher is built, and the policy changes mid-session.
	//
	// Nil leaves subagents ungated, which is what they were before this
	// existed: a delegated write tool then skips an approval the same call
	// would face on the root path.
	Approval func() sdkadapter.ApprovalDeps

	Completer    provider.Completer
	Model        string
	ProviderName string
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
	// ToolRunTimeout is the [tools] tool_run_timeout_seconds knob applied to
	// every nested sub-agent loop's SDK tool registry: the registry-wide run
	// backstop for tools with no declared Capability.Timeout. <= 0 = no
	// registry-wide cap (the SDK's TimeoutNone).
	ToolRunTimeout time.Duration
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
	// non-chat caller (workflow/background paths); CoreMemoryBlockForState
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
	SkillScope AgentSkillScope

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

	// OnToolCancelReady, when set, is forwarded onto every MultiStepHandler
	// this dispatcher registers as that task's
	// subagents.MultiStepHandler.OnToolCancelReady: the per-task sink for
	// the ability to cancel ONE in-flight tool call within ONE running
	// subagent task. Nil (the default for every caller that predates this
	// field) leaves nested loops exactly as before - no cancel-by-ID
	// capability offered for their tool calls. Production callers set this
	// from cliorchestrate.ToolCancelReadyHook(d) for the SAME dispatcher d
	// this Opts value builds handlers on.
	OnToolCancelReady func(ctx context.Context, canceler agent.ToolCanceler)
}

// Authority resolves the full authorized set nested principals are scoped from.
// AuthorityRegistry takes precedence; Registry is the fallback when nothing is
// deferred.
func (o SessionDispatcherOpts) Authority() *tools.Registry {
	if o.AuthorityRegistry == nil {
		return o.Registry
	}
	return o.AuthorityRegistry
}
