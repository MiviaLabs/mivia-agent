package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

func chatFlags(args []string) (noTools, plainUI, staleBypass bool, rest []string) {
	for _, arg := range args {
		switch arg {
		case "--no-tools":
			noTools = true
		case "--plain":
			plainUI = true
		case "--bypass-hook-trust":
			// Accepted and ignored. The flag existed to run hooks that were
			// never confirmed; there is no confirmation to bypass any more.
			// Rejecting it would break the CI configs it was written for, and
			// those are the runs least able to explain a startup failure.
			staleBypass = true
		default:
			rest = append(rest, arg)
		}
	}
	return noTools, plainUI, staleBypass, rest
}

func configureChatWorkspace(sess *chat.Session, root string, useTools bool, tavilyKey string, tc config.ToolsConfig) error {
	if !useTools {
		return nil
	}
	ws, err := workspace.Open(root)
	if err != nil {
		return fmt.Errorf("workspace: %w", err)
	}
	opts := tools.DefaultOptions{
		Workspace:                ws,
		TavilyAPIKey:             tavilyKey,
		RunAllowlist:             tc.RunAllowlist,
		RunAllowlistOnly:         tc.RunAllowlistOnly,
		RunBlocklist:             tc.RunBlocklist,
		DisableTools:             tc.DisableTools,
		EnvAllowlist:             tc.EnvAllowlist,
		EnvAllowlistOnly:         tc.EnvAllowlistOnly,
		EnvBlocklist:             tc.EnvBlocklist,
		EnvAllowKeywordBlocklist: tc.EnvAllowKeywordBlocklist,
		RunTimeoutSec:            tc.RunTimeoutSec,
		MaxReadBytes:             tc.MaxReadBytes,
		MaxWriteKB:               tc.MaxWriteKB,
		MaxOutputBytes:           tc.MaxOutputBytes,
		MaxListDirEntries:        tc.MaxListDirEntries,
		MaxToolResultBytes:       tc.MaxToolResultBytes,
		MaxTavilyResponseBytes:   tc.MaxTavilyResponseBytes,
		MaxFetchKB:               tc.MaxFetchKB,
		// MiB → bytes; resolveToolsConfig already settled 0 → default 256.
		MemoryBackstopBytes: tc.MemoryBackstopMB << 20,
		// RedactToolArgs is NOT plumbed here - the single source of truth
		// is the package atomic set by tools.SetRedactToolArgs at line 40.
		SecretPathPatterns:   tc.SecretPathPatterns,
		SecretPathExceptions: tc.SecretPathExceptions,
		SearchIgnorePatterns: tc.SearchIgnorePatterns,
	}
	sess.Tools = tools.NewDefaultRegistry(opts)
	return nil
}

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
	Context          contextDispatcherWiring
	// Resolved is the live config the deferred-tool tier split and later
	// surface widenings read. Nil means no global [tools] core; a per-agent
	// tools_core still defers, and every surface build then runs with a nil
	// config (zero MaxDepth and budgets, no model catalog).
	Resolved *config.Resolved
}

func attachSessionDispatcher(sess *chat.Session, root, model string, cfg config.SubagentConfig, state *agentSessionState, skillReg *skills.Registry, routing sessionRouting) (func(), error) {
	if sess == nil {
		return func() {}, nil
	}
	sess.SetSwitchGuard(orchestrationSwitchGuard(sess.SessionID))
	binding := sess.CurrentBinding()
	if binding.Completer == nil {
		return nil, fmt.Errorf("dispatcher: nil completer")
	}
	ctx := agentSessionContext{}
	if state != nil {
		ctx = state.context()
	}
	if skillReg == nil {
		var warnings []string
		var err error
		skillReg, warnings, err = loadSessionSkills(root, ctx.AllowProjectSkills)
		if err != nil {
			return nil, fmt.Errorf("load skills: %w", err)
		}
		warnSkillLoad(warnings)
	}
	skillReg = filterSkillRegistryForGate(skillReg, ctx.AllowProjectSkills)
	skillScope := skillScopeFromAgent(ctx.Selected)
	modelCatalog := routing.Catalog
	// The TUI binding must reflect the root agent's policy. Keep skillReg itself
	// complete for explicitly routed task agents, which validate their own scope.
	sess.SetBindingSkillRegistry(filterSkillsForScope(skillReg, skillScope))
	if sess.Tools == nil {
		return func() {}, nil
	}
	surface := scopeAttachedToolSurface(sess, ctx, state, skillReg, routing)
	plan, liveScope := surface.plan, surface.skillScope
	adoptSessionLedgerRepo(sess, cfg, state, routing)
	dispatcher, err := NewSessionDispatcher(SessionDispatcherOpts{
		Registry:                  sess.Tools,
		AuthorityRegistry:         surface.authority,
		Repo:                      ledgerRepoOf(state),
		Completer:                 binding.Completer,
		Model:                     model,
		ProviderName:              binding.ProviderName,
		ModelGeneration:           binding.ModelGeneration,
		ModelGenerationFunc:       sess.CurrentModelGeneration,
		ModelCatalog:              modelCatalog,
		CompleterFactory:          routing.CompleterFactory,
		Config:                    cfg,
		ToolResultCapBytes:        sess.MaxToolResultChars,
		BatchResultBudgetBytes:    sess.BatchResultBudgetBytes,
		WorkspaceRoot:             root,
		MaxContextTokens:          sess.PromptBudget(),
		MaxTokens:                 sess.MaxTokens,
		Budget:                    sess.PromptBudget,
		SharedSQLite:              routing.Context.sharedSQLite,
		ContextPreparationManager: routing.Context.preparation,
		ContextPreparationInput:   routing.Context.preparationInput,
		SkillReg:                  skillReg,
		SkillScope:                liveScope,
		AgentRegistry:             ctx.Registry,
		DeferredTools:             plan.Candidates,
		Session:                   sess,
	})
	if err != nil {
		// No cleanup is handed back on this path, so the store adopted just
		// above would otherwise stay open for the life of the process.
		releaseSessionLedgerRepo(state)
		return nil, fmt.Errorf("dispatcher: %w", err)
	}
	sess.SetDispatcher(dispatcher)
	recordSchemaMass(sess, state, plan, sess.AdmittedTools(), agentNameOf(ctx.Selected), "attach")
	if plan.Deferred() && state != nil {
		sess.SetSurfaceWidener(newSurfaceWidener(sess, routing.Resolved, state))
		sess.SetAdmissionBinding(agentNameOf(ctx.Selected), plan.Digest)
	}
	// Same spool instance the registered read_output tool holds, so a
	// truncation notice minted by the root loop resolves for this session.
	sess.SetRemainderSpool(RemainderSpoolFromRegistry(sess.Tools))
	return sessionSurfaceCleanup(sess, state), nil
}

// adoptSessionLedgerRepo gives the SESSION ownership of the ledger store the
// dispatcher would otherwise open for itself. /agent, /model and every tool
// admission replace the dispatcher, and publication closes the one it replaced
// - which would close the store the carried-over remainder spool reads through.
// Opening it once here, and passing the same repo to every rebuild, keeps the
// spool's store alive for the session. A shared context store makes this moot:
// the ledger adapter borrows it and owns nothing.
func adoptSessionLedgerRepo(sess *chat.Session, cfg config.SubagentConfig, state *agentSessionState, routing sessionRouting) {
	if state == nil || routing.Context.sharedSQLite != nil {
		return
	}
	if contextDispatcherFor(sess, cfg).sharedSQLite != nil {
		return
	}
	repo, owned := openDurableLedgerRepo(cfg, os.Stderr)
	state.LedgerRepo, state.ownedLedgerStore = repo, owned
}

// releaseSessionLedgerRepo closes and forgets the store adoptSessionLedgerRepo
// opened. It exists for the failure path: ownership is taken before the
// dispatcher is built, and a failed build returns no cleanup function, so
// nothing else would ever close the store.
func releaseSessionLedgerRepo(state *agentSessionState) {
	if state == nil {
		return
	}
	if state.ownedLedgerStore != nil {
		_ = state.ownedLedgerStore.Close()
	}
	state.LedgerRepo, state.ownedLedgerStore = nil, nil
}

// sessionSurfaceCleanup closes whatever dispatcher is live at exit, not the
// attach-time one: /agent, /model and every tool admission replace it. The
// session-owned ledger store closes after the dispatcher, whose teardown still
// reads through the repo.
func sessionSurfaceCleanup(sess *chat.Session, state *agentSessionState) func() {
	return func() {
		sess.CloseDispatcher()
		if state != nil && state.ownedLedgerStore != nil {
			_ = state.ownedLedgerStore.Close()
		}
	}
}

// ledgerRepoOf is the session-owned ledger repository, or nil when there is no
// agent state to hold one (tools-off and hand-built callers).
func ledgerRepoOf(state *agentSessionState) ledger.LedgerRepository {
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
	skillScope agentSkillScope
}

// scopeAttachedToolSurface captures the pre-scope base registry, freezes this
// binding's core/deferred tool split, and narrows sess.Tools to the core tier.
//
// Scope is applied BEFORE the dispatcher is built so the dispatcher captures a
// scoped registry: a tool absent from sess.Tools is also absent from the
// dispatcher's executable registry (INV-AG-29 execution denial).
func scopeAttachedToolSurface(sess *chat.Session, ctx agentSessionContext, state *agentSessionState, skillReg *skills.Registry, routing sessionRouting) attachedToolSurface {
	// Snapshot the full post-registration registry BEFORE root agent scope.
	// This is the base for mid-session /agent re-scope; it must include all
	// tools so switching to a wider agent can regain them.
	if state != nil {
		state.ToolBase = sess.Tools.Clone()
	}
	plan := planToolTiers(sess.Tools, ctx.Selected, routing.Resolved)
	if state != nil {
		state.TierPlan = plan
		state.SkillRegFull = skillReg
	}
	// Authority is the root scope without the tier split: deferring a tool
	// withholds its schema from the root model, it does not revoke the session's
	// authority to delegate it.
	authority, disabled := scopedRootRegistry(sess.Tools, ctx.Selected, ctx.Global.MandatoryToolDenylistAdditions)
	// Attach is an entry point, so this is where the operator hears about tool
	// names their agent asks for and this build cannot offer - once, before any
	// turn starts and before the TUI owns the terminal.
	warnDisabledAgentTools(ctx.Selected, disabled)
	sess.Tools = tieredRootRegistry(sess.Tools, ctx.Selected, ctx.Global.MandatoryToolDenylistAdditions, plan, nil)
	applyDeferredToolPrompt(sess, routing.Resolved, plan)
	// Rebuild the skill policy against the final live authority registry (plan
	// 43) so a skill requiring a disabled/denied tool cannot activate, and store
	// it for the TUI slash path.
	liveScope := skillScopeFromAgentAndRegistry(ctx.Selected, authority)
	if state != nil {
		state.setSkillScope(liveScope)
	}
	return attachedToolSurface{plan: plan, authority: authority, skillScope: liveScope}
}

func repl(sess *chat.Session, res *config.Resolved, toolsOn bool, _ *agentSessionState) error {
	printReplBanner(sess, toolsOn)
	defer autoSaveREPL(sess)
	term, err := NewTerminal()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: not a terminal (%v), falling back to line mode\n", err)
		return replLineMode(sess, res, toolsOn)
	}
	defer term.Close()
	r := newREPLRuntime(sess, res, toolsOn, term)
	return r.run()
}

func printReplBanner(sess *chat.Session, toolsOn bool) {
	mode := "chat"
	if toolsOn {
		mode = "agent"
	}
	fmt.Fprintf(os.Stderr, "mivia %s  provider=%s model=%s%s\n", mode, sess.CurrentSelection().ProviderName, sess.CurrentBinding().Model, formatSessionAgentStatus(classicAgentState, sess))
	if toolsOn {
		fmt.Fprintln(os.Stderr, "Tools on. /tools /workspace /help - Ctrl-C cancel or exit at prompt.")
	} else {
		fmt.Fprintln(os.Stderr, "Tools off (--no-tools). /help - Ctrl-C cancel or exit at prompt.")
	}
}

func autoSaveREPL(sess *chat.Session) {
	err := sess.SaveLast()
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠ auto-save failed: %v\n", err)
	}
	writeAutosaveStatus(sess.SessionDir, err)
}

func replLineMode(sess *chat.Session, res *config.Resolved, toolsOn bool) error {
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	defer signal.Stop(sigCh)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if handled, exit, herr := handleSlash(line, sess, res, toolsOn, nil); handled {
			if herr != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", herr)
			}
			if exit {
				return nil
			}
			continue
		}
		if line == "exit" || line == "quit" {
			return nil
		}
		if err := sendLineMode(sess, line, sigCh); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		}
	}
	return sc.Err()
}

func sendLineMode(sess *chat.Session, line string, sigCh <-chan os.Signal) error {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go cancelOnInterrupt(ctx, cancel, done, sigCh)
	usage := sess.ContextUsage()
	fmt.Fprintf(os.Stderr, "  (~%d tokens, %d%% context used)\n", usage.UsedTokens, usage.Percent)
	_, err := sess.SendUser(ctx, line, os.Stdout)
	for _, note := range sess.TakeAdmissionNotes() {
		fmt.Fprintf(os.Stderr, "\n%s\n", note)
	}
	// Read the interrupt BEFORE cancelling. This used to ask ctx.Err() after
	// its own cancel(), so every turn reported "(cancelled)" and returned nil -
	// the turn's real error was discarded on the one surface that has nowhere
	// else to show it, and a durable publication failure looked like the user
	// pressing Ctrl+C.
	interrupted := ctx.Err() != nil
	close(done)
	cancel()
	fmt.Fprintln(os.Stdout)
	if interrupted {
		fmt.Fprintln(os.Stderr, "(cancelled)")
		return nil
	}
	return err
}

func cancelOnInterrupt(ctx context.Context, cancel context.CancelFunc, done <-chan struct{}, sigCh <-chan os.Signal) {
	select {
	case <-sigCh:
		cancel()
	case <-done:
	case <-ctx.Done():
	}
}
