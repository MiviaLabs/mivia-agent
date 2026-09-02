package clichat

import (
	"fmt"
	"os"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	cliagents "github.com/MiviaLabs/mivia-agent/internal/cliagents"
	cliorchestrate "github.com/MiviaLabs/mivia-agent/internal/cliorchestrate"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/mcp"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// attachSessionDispatcher wires NewSessionDispatcher onto the session using the
// shared agent-aware builder (same contract as model switch). skillReg may be
// pre-loaded by the caller so agent/skill collisions were already checked.
// When state is non-nil, ToolBase is captured before root agent scope so
// mid-session /agent can re-scope without losing tools. Agent scope is applied
// BEFORE building the dispatcher so the dispatcher and sess.Tools agree
// (INV-AG-29 execution denial).
// sessionRouting carries what a routed agent needs to bind its own provider:
// the catalog that authorizes a (provider, model) pair and the factory that
// constructs a completer for it. The zero value authorizes nothing, so an
// agent declaring a foreign binding fails closed rather than silently running
// on the session's provider.
type sessionRouting struct {
	Catalog          []config.ProviderModelGroup
	CompleterFactory func(providerName, model string) (provider.Completer, error)
	Context          ContextDispatcherWiring
	// Resolved is the live config the deferred-tool tier split and later
	// surface widenings read. Nil means no global [tools] core; a per-agent
	// tools_core still defers, and every surface build then runs with a nil
	// config (zero MaxDepth and budgets, no model catalog).
	Resolved *config.Resolved
}

// sessionSkillRegistry resolves the skill registry a session starts with,
// loading it when the caller supplied none. The project-source gate is applied
// on both branches: a caller-supplied registry is not a grant.
func sessionSkillRegistry(root string, ctx agentSessionContext, skillReg *skills.Registry) *skills.Registry {
	if skillReg == nil {
		// skills.LoadMarkdownSources degrades an absent or unreadable tree to a
		// warning and never returns an error, so a broken skills directory
		// yields an empty registry rather than refusing to start the session.
		loaded, warnings, _ := cliagents.LoadSessionSkills(root, ctx.AllowProjectSkills)
		cliagents.WarnSkillLoad(warnings)
		skillReg = loaded
	}
	return cliagents.FilterSkillRegistryForGate(skillReg, ctx.AllowProjectSkills)
}

func attachSessionDispatcher(sess *chat.Session, root, model string, cfg config.SubagentConfig, state *AgentSessionState, skillReg *skills.Registry, routing sessionRouting) (func(), error) {
	if sess == nil {
		return func() {}, nil
	}
	sess.SetSwitchGuard(cliorchestrate.OrchestrationSwitchGuard(sess.SessionID))
	binding := sess.CurrentBinding()
	if binding.Completer == nil {
		return nil, fmt.Errorf("dispatcher: nil completer")
	}
	ctx := agentSessionContext{}
	if state != nil {
		ctx = state.Context()
	}
	skillReg = sessionSkillRegistry(root, ctx, skillReg)
	skillScope := cliagents.SkillScopeFromAgent(ctx.Selected)
	modelCatalog := routing.Catalog
	// Keep skillReg complete for explicitly routed task agents.
	sess.SetBindingSkillRegistry(cliagents.FilterSkillsForScope(skillReg, skillScope))
	if sess.Tools == nil {
		return func() {}, nil
	}
	mcpManager, closeMCP, err := cliagents.SetupSessionMCPTools(sess.Tools, routing.Resolved, ctx.Selected)
	if err != nil {
		return nil, fmt.Errorf("MCP tools: %w", err)
	}
	surface := scopeAttachedToolSurface(sess, ctx, state, skillReg, routing)
	plan, liveScope := surface.plan, surface.skillScope
	adoptSessionLedgerRepo(sess, cfg, state, routing)
	dispatcher, err := NewSessionDispatcher(SessionDispatcherOpts{
		// The operator's approval wiring, read per invocation so a nested loop
		// sees the gate installed after this dispatcher was built and any
		// policy change since.
		Approval:                  sess.ApprovalSnapshot,
		Registry:                  sess.Tools,
		AuthorityRegistry:         surface.authority,
		Repo:                      ledgerRepoOf(state),
		Memory:                    MemoryOfFunc(state),
		MemoryConfig:              MemoryConfigOfFunc(state),
		Completer:                 binding.Completer,
		Model:                     model,
		ProviderName:              binding.ProviderName,
		ModelGeneration:           binding.ModelGeneration,
		ModelGenerationFunc:       sess.CurrentModelGeneration,
		ModelCatalog:              modelCatalog,
		CompleterFactory:          routing.CompleterFactory,
		Config:                    cfg,
		MCP:                       cliagents.SessionMCPConfig(routing.Resolved),
		EnsureMCPTools:            cliagents.EnsureMCPServerTools(surface.authority, mcpManager),
		ToolResultCapBytes:        sess.MaxToolResultChars,
		ToolRunTimeout:            sess.ToolRunTimeout,
		BatchResultBudgetBytes:    sess.BatchResultBudgetBytes,
		RefOnlyTools:              sess.RefOnlyTools,
		WorkspaceRoot:             root,
		MaxContextTokens:          sess.PromptBudget(),
		MaxTokens:                 sess.MaxTokens,
		Budget:                    sess.PromptBudget,
		Reasoning:                 sess.ReasoningSetting,
		SharedSQLite:              routing.Context.SharedSQLite,
		ContextPreparationManager: routing.Context.Preparation,
		ContextPreparationInput:   routing.Context.PreparationInput,
		SkillReg:                  skillReg,
		SkillScope:                liveScope,
		AgentRegistry:             ctx.Registry,
		DeferredTools:             plan.Candidates,
		Session:                   sess,
		// Sink publishes invocation lifecycle events to the session bus. The
		// closure reads sess.EventBus at publish time, so it stays correct
		// whenever the bus is installed - which today is JoinHub, on this
		// surface and line mode only. Nothing installs one for the TUI.
		Sink: sessionInvocationSink(sess),
	})
	if err != nil {
		closeMCP()
		// No cleanup is handed back on this path, so the store adopted just
		// above would otherwise stay open for the life of the process.
		releaseSessionLedgerRepo(state)
		return nil, fmt.Errorf("dispatcher: %w", err)
	}
	return attachBuiltSessionDispatcher(sess, state, dispatcher, mcpManager, plan, ctx, routing, closeMCP), nil
}

// attachBuiltSessionDispatcher wires a built dispatcher onto the session and
// returns the cleanup closure. The remainder spool is the same instance the
// registered read_output tool holds, so a truncation notice minted by the
// root loop resolves for this session.
func attachBuiltSessionDispatcher(sess *chat.Session, state *AgentSessionState, dispatcher *runtime.Dispatcher, mcpManager *mcp.Manager, plan toolTierPlan, ctx agentSessionContext, routing sessionRouting, closeMCP func()) func() {
	sess.SetDispatcher(dispatcher)
	if state != nil {
		state.MCPManager = mcpManager
	}
	cliagents.RecordSchemaMass(sess, state, plan, sess.AdmittedTools(), cliagents.AgentNameOf(ctx.Selected), "attach")
	if plan.Deferred() && state != nil {
		sess.SetSurfaceWidener(cliagents.NewSurfaceWidener(sess, routing.Resolved, state))
		sess.SetAdmissionBinding(cliagents.AgentNameOf(ctx.Selected), plan.Digest)
	}
	sess.SetRemainderSpool(RemainderSpoolFromRegistry(sess.Tools))
	cleanup := sessionSurfaceCleanup(sess, state)
	return func() { cleanup(); closeMCP() }
}

// adoptSessionLedgerRepo gives the SESSION ownership of the ledger store the
// dispatcher would otherwise open for itself. /agent, /model and every tool
// admission replace the dispatcher, and publication closes the one it replaced
// - which would close the store the carried-over remainder spool reads through.
// Opening it once here, and passing the same repo to every rebuild, keeps the
// spool's store alive for the session. A shared context store makes this moot:
// the ledger adapter borrows it and owns nothing.
func adoptSessionLedgerRepo(sess *chat.Session, cfg config.SubagentConfig, state *AgentSessionState, routing sessionRouting) {
	if state == nil || routing.Context.SharedSQLite != nil {
		return
	}
	if contextDispatcherFor(sess, cfg).SharedSQLite != nil {
		return
	}
	if state.LedgerRepo != nil {
		// Adopted earlier in this startup (before the workflow tool wiring, so
		// the workflow engine sees the session's repo instance). Keep it: every
		// surface rebuild must hand the dispatcher the SAME repo, and the
		// workflow child-run registration stamps exactly this instance.
		return
	}
	repo, owned := cliorchestrate.OpenDurableLedgerRepo(cfg, os.Stderr)
	state.AdoptLedgerRepo(repo, owned)
}

// releaseSessionLedgerRepo closes and forgets the store adoptSessionLedgerRepo
// opened. It exists for the failure path: ownership is taken before the
// dispatcher is built, and a failed build returns no cleanup function, so
// nothing else would ever close the store.
func releaseSessionLedgerRepo(state *AgentSessionState) {
	if state == nil {
		return
	}
	state.ReleaseOwnedLedgerRepo()
}

// sessionSurfaceCleanup closes whatever dispatcher is live at exit, not the
// attach-time one: /agent, /model and every tool admission replace it. The
// session-owned ledger store closes after the dispatcher, whose teardown still
// reads through the repo.
func sessionSurfaceCleanup(sess *chat.Session, state *AgentSessionState) func() {
	return func() {
		sess.CloseDispatcher()
		if state != nil {
			state.CloseOwnedLedgerStore()
		}
	}
}

// ledgerRepoOf is the session-owned ledger repository, or nil when there is no
// agent state to hold one (tools-off and hand-built callers).
func ledgerRepoOf(state *AgentSessionState) ledger.LedgerRepository {
	if state == nil {
		return nil
	}
	return state.LedgerRepo
}

// attachedToolSurface is what scopeAttachedToolSurface decided: the frozen tier
// split, the full authorized set nested principals are scoped from, and the
// skill policy built against it.
type attachedToolSurface struct {
	plan       toolTierPlan
	authority  *tools.Registry
	skillScope AgentSkillScope
}

// scopeAttachedToolSurface captures the pre-scope base registry, freezes this
// binding's core/deferred tool split, and narrows sess.Tools to the core tier.
//
// Scope is applied BEFORE the dispatcher is built so the dispatcher captures a
// scoped registry: a tool absent from sess.Tools is also absent from the
// dispatcher's executable registry (INV-AG-29 execution denial).
func scopeAttachedToolSurface(sess *chat.Session, ctx agentSessionContext, state *AgentSessionState, skillReg *skills.Registry, routing sessionRouting) attachedToolSurface {
	// Snapshot the full post-registration registry BEFORE root agent scope.
	// This is the base for mid-session /agent re-scope; it must include all
	// tools so switching to a wider agent can regain them.
	if state != nil {
		state.ToolBase = sess.Tools.Clone()
		// A closure, not a snapshot: state.ToolBase can be replaced later
		// (agent/model switch), and UnadmittedToolHandler (session_turn_surface.go)
		// must always read the CURRENT full tool set to serve a deferred call
		// synchronously.
		sess.ToolBaseResolver = func() *tools.Registry { return state.ToolBase }
		// state.ToolBase is the PRE-scope clone, so the operator's denylist
		// has not been applied to it. The deferred path resolves from it and
		// would otherwise execute a denied name that every other layer
		// refuses - including this function's own contract two comments up.
		sess.ToolDenylist = ctx.Global.MandatoryToolDenylistAdditions
	}
	plan := cliagents.PlanToolTiers(sess.Tools, ctx.Selected, routing.Resolved)
	if state != nil {
		state.TierPlan = plan
		state.SkillRegFull = skillReg
	}
	// Authority is the root scope without the tier split: deferring a tool
	// withholds its schema from the root model, it does not revoke the session's
	// authority to delegate it.
	authority, _ := cliagents.ScopedRootRegistry(sess.Tools, ctx.Selected, ctx.Global.MandatoryToolDenylistAdditions)
	// Attach is an entry point, so this is where the operator hears about tool
	// names their agent asks for and this build cannot offer - once, before any
	// turn starts and before the TUI owns the terminal. The disabled set comes
	// from the agent's effective tools (disabledForAgent), not from the scoped
	// build: the scope intersects with the registry, so its own report is
	// always empty.
	cliagents.WarnDisabledAgentTools(ctx.Selected, cliagents.DisabledForAgent(ctx.Selected, sess.Tools))
	cliagents.PinAttachAdvertisedToolSpecs(sess, ctx.Selected, plan, ctx.Registry)
	sess.Tools = cliagents.TieredRootRegistry(sess.Tools, ctx.Selected, ctx.Global.MandatoryToolDenylistAdditions, plan, nil)
	// Same attach-time refresh: the scoped tool surface changed the wire
	// prefix; applyDeferredToolPrompt below republishes the prompt through
	// SetAgentSettings, which also recaptures (audit RC-1).
	sess.RefreshPrefixIdentity()
	cliagents.ApplyDeferredToolPrompt(sess, routing.Resolved, plan, state)
	// Rebuild the skill policy against the final live authority registry (plan
	// 43) so a skill requiring a disabled/denied tool cannot activate, and store
	// it for the TUI slash path.
	liveScope := cliagents.SkillScopeFromAgentAndRegistry(ctx.Selected, authority)
	if state != nil {
		state.SetSkillScope(liveScope)
	}
	return attachedToolSurface{plan: plan, authority: authority, skillScope: liveScope}
}

func repl(sess *chat.Session, res *config.Resolved, toolsOn bool, _ *AgentSessionState, jsonMode, quiet bool) error {
	printReplBanner(sess, toolsOn, quiet)
	defer autoSaveREPL(sess)
	term, err := NewTerminal()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: not a terminal (%v), falling back to line mode\n", err)
		return replLineMode(sess, res, toolsOn, jsonMode)
	}
	defer term.Close()
	defer startClassicReplHub(sess)()
	r := newREPLRuntime(sess, res, toolsOn, term)
	return r.run()
}

func printReplBanner(sess *chat.Session, toolsOn, quiet bool) {
	if quiet {
		return
	}
	mode := "chat"
	if toolsOn {
		mode = "agent"
	}
	fmt.Fprintf(os.Stderr, "mivia %s  provider=%s model=%s%s\n", mode, sess.CurrentSelection().ProviderName, sess.CurrentBinding().Model, cliagents.FormatSessionAgentStatus(cliagents.ClassicAgentState, sess))
	if toolsOn {
		fmt.Fprintln(os.Stderr, "Tools on. /tools /workspace /help - Ctrl-C cancel or exit at prompt.")
	} else {
		fmt.Fprintln(os.Stderr, "Tools off (--no-tools). /help - Ctrl-C cancel or exit at prompt.")
	}
}

func autoSaveREPL(sess *chat.Session) {
	if err := sess.SaveLast(); err != nil {
		fmt.Fprintf(os.Stderr, "⚠ auto-save failed: %v\n", err)
	}
}
