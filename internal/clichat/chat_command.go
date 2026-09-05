package clichat

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	cliagents "github.com/MiviaLabs/mivia-agent/internal/cliagents"
	cliorchestrate "github.com/MiviaLabs/mivia-agent/internal/cliorchestrate"
	"github.com/MiviaLabs/mivia-agent/internal/cliworktree"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"golang.org/x/term"
)

func runChat(args []string) error {
	invocation, err := parseChatInvocation(args)
	if err != nil {
		return err
	}
	workspaceRoot, err := chatWorkspaceRoot(invocation.workspacePath)
	if err != nil {
		return err
	}
	loadOpts := config.LoadOptions{
		ConfigPath: invocation.configPath, ProviderOverride: invocation.provider,
		ModelOverride: invocation.model, WorkspaceRoot: workspaceRoot,
		AllowMissingConfig: true, AutoBootstrapUserConfig: true,
	}
	res, err := config.Load(loadOpts)
	if err != nil {
		return err
	}
	res, err = ensureChatAPIKey(res, loadOpts, os.Stdout, os.Stdin)
	if err != nil {
		return err
	}
	return runConfiguredChat(invocation, res)
}

var runConfiguredChatOnceImpl = runConfiguredChatOnce
var loadConfigForRestart = config.Load
var classifyMissingMarkerForBind = cliworktree.ClassifyMissingWorktreeMarker

func runConfiguredChat(invocation chatInvocation, res *config.Resolved) error {
	configPath := invocation.configPath
	if configPath == "" {
		configPath = os.Getenv("MIVIA_CONFIG")
	}
	if configPath != "" {
		abs, err := filepath.Abs(config.ExpandPath(configPath))
		if err != nil {
			return fmt.Errorf("resolve config path: %w", err)
		}
		invocation.configPath = abs
	}
	if root, err := chatRepositoryRoot(invocation.workspacePath); err == nil {
		storePath, err := repositorySessionStorePath(root, invocation, res)
		if err != nil {
			return fmt.Errorf("resolve repository session store: %w", err)
		}
		invocation.repositorySessionStorePath = storePath
	}
	for {
		runInvocation := invocation
		invocation.resumeSessionName = ""
		err := runConfiguredChatOnceImpl(runInvocation, res)
		var restart workspaceRestartError
		if !errors.As(err, &restart) {
			return err
		}
		if err := validateWorkspaceRestart(restart, invocation); err != nil {
			return fmt.Errorf("validate workspace restart: %w", err)
		}
		dir, resumeSessionName, worktreeInstance := restart.WorkspaceRestartInfo()
		invocation.workspacePath = dir
		invocation.resumeSessionName = resumeSessionName
		invocation.expectedWorktreeInstance = worktreeInstance
		if err := os.Chdir(dir); err != nil {
			return fmt.Errorf("enter restarted workspace: %w", err)
		}
		reloaded, err := loadConfigForRestart(config.LoadOptions{
			ConfigPath:         invocation.configPath,
			ProviderOverride:   invocation.provider,
			ModelOverride:      invocation.model,
			WorkspaceRoot:      invocation.workspacePath,
			AllowMissingConfig: true,
		})
		if err != nil {
			return err
		}
		res = reloaded
	}
}

// workspaceRestartError is satisfied by *WorkspaceRestart today, and will be
// satisfied by *legacytui.WorkspaceRestart once that type moves out of this
// package. Lets internal/cli detect a restart via errors.As without needing
// a same-package concrete type.
type workspaceRestartError interface {
	error
	WorkspaceRestartInfo() (dir, resumeSessionName string, wt contextstate.WorktreeInstance)
}

func validateWorkspaceRestart(restart workspaceRestartError, invocation chatInvocation) error {
	dir, _, worktreeInstance := restart.WorkspaceRestartInfo()
	if worktreeInstance.IsZero() {
		return nil
	}
	root, err := chatRepositoryRoot(dir)
	if err != nil {
		return contextstate.ErrWorktreeDeleted
	}
	storePath := invocation.repositorySessionStorePath
	if storePath == "" {
		storePath, err = repositorySessionStorePath(root, invocation, &config.Resolved{})
		if err != nil {
			return err
		}
	}
	store, err := openContextStorePath(storePath)
	if err != nil {
		return err
	}
	defer store.Close()
	return cliworktree.ValidateExpectedWorktreeInstanceInStore(store, root, dir, worktreeInstance)
}

// configureSessionWorkspace wires workspace tools and memory into the chat
// session and attaches the background memory index reconciler. The returned
// cleanup stops the reconciler first and then closes the store, so no sync
// can be in flight while the index closes. This is the reconciler's sole
// production start site: the one-shot ConfigureChatWorkspace callers (compact,
// sessions) never attach one and keep pull-only reads.
func configureSessionWorkspace(sess *chat.Session, wsRoot string, useTools bool, res *config.Resolved, agentState *AgentSessionState, invocation chatInvocation) (func(), error) {
	memClose, err := cliagents.ConfigureChatWorkspace(sess, wsRoot, useTools, res, agentState, invocation.quiet, chatFullDisk(invocation, wsRoot), true)
	if err != nil {
		releaseSessionLedgerRepo(agentState)
		return nil, err
	}
	stop, ok := cliagents.StartMemoryIndexReconciler(agentState.Memory, config.SaturatingSeconds(res.Memory.IndexRefreshIntervalSeconds))
	if !ok {
		return memClose, nil
	}
	return func() {
		stop()
		memClose()
	}, nil
}

func runConfiguredChatOnce(invocation chatInvocation, res *config.Resolved) error {
	useTools, err := prepareChatStartup(res, invocation)
	if err != nil {
		return err
	}
	wsRoot, err := enterChatWorkspace(invocation.workspacePath)
	if err != nil {
		return err
	}

	skillReg, err := loadChatSkills(wsRoot)
	if err != nil {
		return err
	}
	agentState, err := prepareAgentSession(wsRoot, invocation.agent, skillReg)
	if err != nil {
		return err
	}
	cliagents.ApplyWorkspacePromptGate(res, agentState.Global)
	releaseHooks, err := InstallHookSessionFunc(wsRoot, invocation.staleBypass, invocation.quiet)
	if err != nil {
		return err
	}
	defer releaseHooks()
	// The get_diagnostics disclosure prints only when tools are on - the same condition under which the tool is registered.
	if useTools {
		cliagents.LogDiagnosticsCommandsOnce(os.Stderr, res.Tools, invocation.quiet)
	}
	res.SystemPrompt = rootPromptForSession(useTools, res, agentState.Registry)
	comp, err := provider.New(res)
	if err != nil {
		return err
	}
	sess := chat.NewSession(res, comp)
	sess.UseTools = useTools
	applySessionApprovalPolicy(sess, invocation, res)
	cliagents.InstallSessionIdentity(sess, agentState)
	// BaseSystemPrompt, not SystemPrompt (plan 77, E3): equivalent right
	// now (NewSession sets both identically and no compose has happened
	// yet), but reading the field that's guaranteed memory-block-free stays
	// correct regardless of future ordering changes.
	agentState.BaselinePrompt = sess.BaseSystemPrompt
	agentState.BaselineMaxSteps = sess.MaxStepsValue()
	agentState.BaselineCaptured = true
	cliorchestrate.SetActiveSessionCaller(runtime.Caller{SessionID: sess.SessionID})
	// Adopt the session ledger store before the tool wiring (see
	// adoptSessionRepoForTools): child runs stamp this instance.
	adoptSessionRepoForTools(sess, useTools, res, agentState)
	memClose, err := configureSessionWorkspace(sess, wsRoot, useTools, res, agentState, invocation)
	if err != nil {
		return err
	}
	defer memClose()
	cliagents.ApplySelectedAgentPrompt(sess, res, agentState.Selected, agentState)
	contextStore, err := setupChatSessionContext(sess, wsRoot, invocation, res)
	if err != nil {
		releaseSessionLedgerRepo(agentState)
		return err
	}
	defer contextStore.Close()
	// Capture pointer so /agent and model-switch rebuilds see updates.
	sess.SetBindingFactory(cliagents.ChatBindingFactory(sess, res, wsRoot, agentState))
	if invocation.session != "" {
		if err := resumeChatSession(sess, res, invocation.session); err != nil {
			releaseSessionLedgerRepo(agentState)
			return err
		}
	}
	contextWiring := contextDispatcherFor(sess, res.Subagents)
	cleanup, err := attachSessionDispatcher(sess, wsRoot, res.Model, res.Subagents, agentState, skillReg, sessionRouting{
		Catalog: res.ModelCatalog(), CompleterFactory: cliagents.NewProviderCompleterFactory(res),
		Context: contextWiring, Resolved: res,
	})
	if err != nil {
		return err
	}
	defer cleanup()
	return dispatchChatSurface(invocation, sess, wsRoot, res, useTools, agentState)
}

// resumeChatSession loads a saved session and refreshes the summarizer
// against whatever binding that Load actually publishes. setupChatSessionContext
// (enableSessionContext) captured the summarizer once, before this runs,
// against the session's startup binding - a resumed session may carry a
// different saved provider/model, and without this refresh compaction for
// the rest of the process keeps summarizing through the pre-resume
// model/completer. Mirrors what uiadapter/session_pool.go does after its own
// sess.Load.
func resumeChatSession(sess *chat.Session, res *config.Resolved, session string) error {
	if err := sess.Load(session); err != nil {
		return fmt.Errorf("--session %q: %w (omit --session to start a new session under a system-assigned id)", session, err)
	}
	cliagents.RefreshSummarizerAfterModelSwitch(sess, res)
	// Same reasoning as the summarizer refresh above: enableSessionContext
	// seeded token-estimate calibration once, before Load published this
	// session's real saved binding. See RefreshCalibrationAfterModelSwitch's
	// doc comment for what a stale seed does to the context gauge.
	sess.RefreshCalibrationAfterModelSwitch(context.Background())
	return nil
}

// applySessionApprovalPolicy resolves the session's initial approval policy,
// highest precedence first: an explicit --yolo flag, an explicit
// --approval-policy flag, then the persisted [approvals] config
// (DefaultMode, the TUI settings screen's single source of truth, with
// legacy Policy as a fallback alias - see ApprovalsConfig.ApprovalPolicy).
// This runs on every session construction, including session resume
// (runConfiguredChatOnce rebuilds res from the on-disk config before
// calling this), so a persisted "always"/"deny" choice is honored both on
// a fresh start and after --session/resume, not just for a brand-new chat.
//
// It goes through Session.SetBaseApprovalPolicy (not a direct field write)
// so BaseApprovalPolicy is seeded from the SAME resolved value as
// ApprovalPolicy. Session.ToggleYOLO ("/yolo") restores BaseApprovalPolicy
// when the user disables YOLO mode; leaving it unseeded here meant a
// direct sess.ApprovalPolicy write (once the intentional --yolo/
// --approval-policy overrides no longer apply mid-session) always fell
// back to ToggleYOLO's own hardcoded write-only default on toggle-off,
// silently discarding the persisted config policy (e.g. the new "auto"
// shipped default, or an explicit "deny") the very first time a user
// toggled YOLO off.
func applySessionApprovalPolicy(sess *chat.Session, invocation chatInvocation, res *config.Resolved) {
	var policy string
	switch {
	case invocation.yolo:
		policy = config.ApprovalPolicyAuto
	case invocation.approvalPolicy != "":
		policy = config.ApprovalsConfig{Policy: invocation.approvalPolicy}.ApprovalPolicy()
	case res != nil:
		policy = res.Approvals.ApprovalPolicy()
	default:
		return
	}
	sess.SetBaseApprovalPolicy(policy)
	sess.SetApprovalPolicy(policy)
}

// dispatchChatSurface picks and runs the surface (one-shot, classic REPL, or
// TUI) a fully-built session dispatches to. Split out of
// runConfiguredChatOnce to keep that setup function under the repo's per-
// function line budget.
func dispatchChatSurface(invocation chatInvocation, sess *chat.Session, wsRoot string, res *config.Resolved, useTools bool, agentState *AgentSessionState) error {
	// Releases sess's context-lease heartbeat (if armed) on every return path
	// out of this function - the one choke point one-shot, REPL, and TUI all
	// return through. Without this, a session that ran long enough for even
	// one heartbeat tick looked "live" to a rival ReclaimSession for the rest
	// of the lease TTL after this process quit cleanly, so an ordinary "quit,
	// then resume" shortly after was refused as already-live. Best-effort and
	// bounded: see Session.ReleaseContextLease.
	// Before any surface runs a turn: every surface reaches this function, and
	// a publish with no bus is a silent no-op. See attachSessionEventBus.
	attachSessionEventBus(sess)
	// Registers this session's bus in the session-keyed registry so
	// emitSubagentProgress (package-level, no session of its own) can
	// publish this session's subagent lifecycle events onto it. This is
	// the single choke point -p/REPL/TUI all pass through - TUILauncherFunc
	// does not re-enter it - so every surface gets the binding exactly
	// once per dispatch, undone unconditionally on return.
	defer RegisterSessionBus(sess.SessionID, sess.EventBus)()
	defer sess.ReleaseContextLease(context.Background())
	if invocation.jsonMode {
		if err := validateJSONModeInvocation(invocation); err != nil {
			return err
		}
	}
	if invocation.prompt != "" {
		defer attachCLISync(sess, wsRoot, res)()
		return oneShot(sess, invocation.prompt, useTools, res, invocation.quiet)
	}
	// Classic REPL /agent uses package state; TUI stores agentState on the model.
	cliagents.ClassicAgentState = agentState
	defer func() { cliagents.ClassicAgentState = nil }()
	if invocation.plainUI || !term.IsTerminal(int(os.Stdin.Fd())) || strings.EqualFold(os.Getenv("TERM"), "dumb") {
		defer attachCLISync(sess, wsRoot, res)()
		return repl(sess, res, useTools, agentState, invocation.jsonMode, invocation.quiet)
	}
	if TUILauncherFunc == nil {
		return fmt.Errorf("chat: TUI backend is unwired")
	}
	return TUILauncherFunc(sess, res, useTools, agentState, invocation.resumeSessionName)
}

func enterChatWorkspace(path string) (string, error) {
	if path == "" {
		path = "."
	}
	root, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve workspace: %w", err)
	}
	if err := os.Chdir(root); err != nil {
		return "", fmt.Errorf("enter workspace: %w", err)
	}
	return root, nil
}

func chatWorkspaceRoot(path string) (string, error) {
	if path == "" {
		path = "."
	}
	root, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve workspace: %w", err)
	}
	return root, nil
}

// loadChatSkills loads session skills under the user gate before agent resolve
// so skill-name collisions fail closed. Project skills load only when the gate
// is on, so they cannot shadow then erase user skills.
func loadChatSkills(wsRoot string) (*skills.Registry, error) {
	globalPreview, err := config.LoadAgentsGlobal(wsRoot)
	if err != nil {
		return nil, err
	}
	skillReg, skillWarnings, err := cliagents.LoadSessionSkills(wsRoot, globalPreview.LoadWorkspaceConfig)
	if err != nil {
		return nil, fmt.Errorf("load skills: %w", err)
	}
	cliagents.WarnSkillLoad(skillWarnings)
	return skillReg, nil
}

// prepareAgentSession loads and optionally selects a named agent definition.
func prepareAgentSession(wsRoot, agentFlag string, skillReg *skills.Registry) (*AgentSessionState, error) {
	loaded, err := cliagents.LoadAgentDefinitions(wsRoot, agentFlag, skillReg)
	if err != nil {
		return nil, err
	}
	cliagents.WarnAgentLoad(loaded.Warnings)
	return &AgentSessionState{
		Global:             loaded.Global,
		Selected:           loaded.Selected,
		AllowProjectSkills: loaded.Global.LoadWorkspaceConfig,
		Registry:           loaded.Registry,
		WorkspaceRoot:      wsRoot,
	}, nil
}

// adoptSessionRepoForTools adopts the session ledger store before the tool
// wiring when tools are on: the workflow engine stamps this instance on
// child runs, and the attach reuses it. Tools off means the attach never
// adopts either, so adopting here would open a store nothing ever closes.
func adoptSessionRepoForTools(sess *chat.Session, useTools bool, res *config.Resolved, agentState *AgentSessionState) {
	if !useTools {
		return
	}
	adoptSessionLedgerRepo(sess, res.Subagents, agentState, sessionRouting{Context: contextDispatcherFor(sess, res.Subagents), Resolved: res})
}
