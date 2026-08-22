package clichat

import (
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
	"github.com/MiviaLabs/mivia-agent/internal/redact"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"golang.org/x/term"
)

// applyPrivacyPolicy installs the process-wide privacy settings.
//
// Tool-argument redaction is opt-in and read from BOTH [privacy] and [tools]
// so either TOML path works. The redaction policy is nil when the workspace
// configured no patterns, which redacts nothing - see rule 10.
func applyPrivacyPolicy(res *config.Resolved) {
	tools.SetRedactToolArgs(res.Privacy.RedactToolArgs || res.Tools.RedactToolArgs)
	redact.SetPolicy(res.RedactionPolicy)
	applyContextLimits(res)
}

// applyContextLimits installs the operator's durable ceilings process-wide.
// It sits beside the redaction policy deliberately: both are workspace policy
// this binary must not invent, and a process that configures neither runs
// uncapped and unredacted rather than under a compiled-in guess.
func applyContextLimits(res *config.Resolved) {
	contextstate.SetLimits(contextstate.Limits{
		SourceEventBytes:        res.Context.MaxSourceEventBytes,
		CheckpointBytes:         res.Context.MaxCheckpointBytes,
		CommitEvents:            res.Context.MaxCommitEvents,
		CommitEventBytes:        res.Context.MaxCommitEventBytes,
		SessionStateBytes:       res.Context.MaxSessionStateBytes,
		ExportBytes:             res.Context.MaxExportBytes,
		SummaryMetadataBytes:    res.Context.SummaryMetadataBytes,
		CheckpointMetadataBytes: res.Context.CheckpointMetadataBytes,
	})
}

type chatInvocation struct {
	prompt, provider, model, configPath, workspacePath, resumeSessionName, repositorySessionStorePath string
	agent                                                                                             string
	// session is --session <name>: resume a saved session (by the session_id
	// or snapshot name `mivia sessions list` reports) before the surface
	// starts. An unknown name fails closed - see runConfiguredChatOnce -
	// rather than silently starting a fresh session under that name, because
	// this codebase never lets a caller choose a session's identity (new
	// sessions always mint a fresh id via RotateSessionID); --session only
	// resumes an id/name that already exists.
	session                  string
	expectedWorktreeInstance contextstate.WorktreeInstance
	// staleBypass records that the removed --bypass-hook-trust flag was passed,
	// so the session can say the flag no longer does anything.
	staleBypass                            bool
	allowProgram, denyProgram, disableTool []string
	allowEnvVar, denyEnvVar                []string
	noTools, plainUI                       bool
	// quiet is --quiet: suppress informational startup notices on stderr
	// (limits summary, lifecycle-hooks armed notice, diagnostics commands
	// line, one-shot/REPL banner). Genuine config warnings and workflow
	// session-recovery diagnostics still print.
	quiet bool
	// jsonMode is --json: reframe line-mode's stdout as NDJSON events instead
	// of raw streamed text. Only valid for the non-interactive piped-stdin
	// line-mode path - runConfiguredChatOnce rejects it for one-shot -p and
	// for the interactive TUI/classic-REPL paths.
	jsonMode bool
	// fullDisk is --full-disk: lift workspace confinement so file tools may
	// access anywhere on the filesystem. Operator-invocation only.
	fullDisk bool
}

func runChat(args []string) error {
	invocation, err := parseChatInvocation(args)
	if err != nil {
		return err
	}
	workspaceRoot, err := chatWorkspaceRoot(invocation.workspacePath)
	if err != nil {
		return err
	}
	res, err := config.Load(config.LoadOptions{
		ConfigPath: invocation.configPath, ProviderOverride: invocation.provider,
		ModelOverride: invocation.model, WorkspaceRoot: workspaceRoot, AllowMissingConfig: true,
	})
	if err != nil {
		return err
	}
	return runConfiguredChat(invocation, res)
}

func parseChatInvocation(args []string) (chatInvocation, error) {
	var invocation chatInvocation
	var err error
	invocation.prompt, args, _, err = FlagValueFunc(args, "-p", "--prompt")
	if err != nil {
		return chatInvocation{}, err
	}
	invocation.provider, args, _, err = FlagValueFunc(args, "--provider")
	if err != nil {
		return chatInvocation{}, err
	}
	invocation.model, args, _, err = FlagValueFunc(args, "--model")
	if err != nil {
		return chatInvocation{}, err
	}
	invocation.configPath, args, _, err = FlagValueFunc(args, "--config")
	if err != nil {
		return chatInvocation{}, err
	}
	invocation.workspacePath, args, _, err = FlagValueFunc(args, "--workspace")
	if err != nil {
		return chatInvocation{}, err
	}
	invocation.agent, args, _, err = FlagValueFunc(args, "--agent")
	if err != nil {
		return chatInvocation{}, err
	}
	invocation.session, args, _, err = FlagValueFunc(args, "--session")
	if err != nil {
		return chatInvocation{}, err
	}
	invocation.allowProgram, args, _, err = FlagVarFunc(args, "--allow-program")
	if err != nil {
		return chatInvocation{}, err
	}
	invocation.denyProgram, args, _, err = FlagVarFunc(args, "--deny-program")
	if err != nil {
		return chatInvocation{}, err
	}
	invocation.disableTool, args, _, err = FlagVarFunc(args, "--disable-tool")
	if err != nil {
		return chatInvocation{}, err
	}
	invocation.allowEnvVar, args, _, err = FlagVarFunc(args, "--allow-env-var")
	if err != nil {
		return chatInvocation{}, err
	}
	invocation.denyEnvVar, args, _, err = FlagVarFunc(args, "--deny-env-var")
	if err != nil {
		return chatInvocation{}, err
	}
	invocation.noTools, invocation.plainUI, invocation.staleBypass, invocation.jsonMode, invocation.quiet, invocation.fullDisk, args = chatFlags(args)
	if len(args) > 0 {
		return chatInvocation{}, fmt.Errorf("chat: unexpected arguments: %v", args)
	}
	return invocation, nil
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

// prepareChatStartup runs the pre-session startup policy: the API key gate,
// the tool/override application, the privacy policy, and the once-per-process
// effective-limits notice. Split out of runConfiguredChatOnce to keep the setup
// path under the per-function line budget.
func prepareChatStartup(res *config.Resolved, invocation chatInvocation) (bool, error) {
	if (!res.APIKeySet || strings.TrimSpace(res.APIKey) == "") && !(res.ProviderName == "ollama" && config.IsOllamaLoopback(res.BaseURL)) {
		return false, fmt.Errorf("missing API key: set %s in environment or env file (see mivia doctor)", res.APIKeyEnv)
	}
	applyChatToolOverrides(res, invocation.allowProgram, invocation.denyProgram, invocation.disableTool, invocation.allowEnvVar, invocation.denyEnvVar)
	useTools := !invocation.noTools
	applyPrivacyPolicy(res)
	logEffectiveLimitsOnce(os.Stderr, res, invocation.quiet)
	if invocation.fullDisk && !invocation.quiet {
		fmt.Fprintln(os.Stderr, "workspace: FULL DISK ACCESS — file tools are not confined to the workspace")
	}
	return useTools, nil
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
	if strings.TrimSpace(res.SystemPrompt) == "" {
		if useTools {
			res.SystemPrompt = buildAgentPrompt(res.Subagents)
		} else {
			res.SystemPrompt = defaultSystemPrompt
		}
	}
	comp, err := provider.New(res)
	if err != nil {
		return err
	}
	sess := chat.NewSession(res, comp)
	sess.UseTools = useTools
	cliagents.InstallSessionIdentity(sess, agentState)
	// BaseSystemPrompt, not SystemPrompt (plan 77, E3): equivalent right
	// now (NewSession sets both identically and no compose has happened
	// yet), but reading the field that's guaranteed memory-block-free stays
	// correct regardless of future ordering changes.
	agentState.BaselinePrompt = sess.BaseSystemPrompt
	agentState.BaselineMaxSteps = sess.MaxStepsValue()
	agentState.BaselineCaptured = true
	cliorchestrate.SetActiveSessionCaller(runtime.Caller{SessionID: sess.SessionID})
	memClose, err := cliagents.ConfigureChatWorkspace(sess, wsRoot, useTools, res, agentState, invocation.quiet, invocation.fullDisk, true)
	if err != nil {
		return err
	}
	defer memClose()
	cliagents.ApplySelectedAgentPrompt(sess, res, agentState.Selected, agentState)
	contextStore, err := setupChatSessionContext(sess, wsRoot, invocation, res)
	if err != nil {
		return err
	}
	defer contextStore.Close()
	// Capture pointer so /agent and model-switch rebuilds see updates.
	sess.SetBindingFactory(cliagents.ChatBindingFactory(sess, res, wsRoot, agentState))
	if invocation.session != "" {
		if err := sess.Load(invocation.session); err != nil {
			return fmt.Errorf("--session %q: %w (omit --session to start a new session under a system-assigned id)", invocation.session, err)
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
	return dispatchChatSurface(invocation, sess, res, useTools, agentState)
}

// dispatchChatSurface picks and runs the surface (one-shot, classic REPL, or
// TUI) a fully-built session dispatches to. Split out of
// runConfiguredChatOnce to keep that setup function under the repo's per-
// function line budget.
func dispatchChatSurface(invocation chatInvocation, sess *chat.Session, res *config.Resolved, useTools bool, agentState *AgentSessionState) error {
	if invocation.jsonMode {
		if err := validateJSONModeInvocation(invocation); err != nil {
			return err
		}
	}
	if invocation.prompt != "" {
		return oneShot(sess, invocation.prompt, useTools, res, invocation.quiet)
	}
	// Classic REPL /agent uses package state; TUI stores agentState on the model.
	cliagents.ClassicAgentState = agentState
	defer func() { cliagents.ClassicAgentState = nil }()
	if invocation.plainUI || !term.IsTerminal(int(os.Stdin.Fd())) || strings.EqualFold(os.Getenv("TERM"), "dumb") {
		return repl(sess, res, useTools, agentState, invocation.jsonMode, invocation.quiet)
	}
	if TUILauncherFunc == nil {
		return fmt.Errorf("chat: TUI backend is unwired")
	}
	return TUILauncherFunc(sess, res, useTools, agentState, invocation.resumeSessionName)
}

// validateJSONModeInvocation fails closed on --json combined with any path
// other than non-interactive piped line-mode: one-shot -p mode never reaches
// the REPL at all, and the interactive TUI/classic-REPL paths (stdin is a
// real terminal) print prompts, banners and rendered UI to stdout that would
// be interleaved with - and indistinguishable from - the NDJSON stream.
// --json's NDJSON framing (see ndjsonEvent) is only meaningful when stdout is
// nothing but that stream, which line-mode is the one path that guarantees.
func validateJSONModeInvocation(invocation chatInvocation) error {
	if invocation.prompt != "" {
		return fmt.Errorf("chat: --json is not supported with -p/--prompt (one-shot mode); it only applies to non-interactive piped chat")
	}
	if term.IsTerminal(int(os.Stdin.Fd())) {
		return fmt.Errorf("chat: --json is not supported for the interactive REPL/TUI; pipe input via stdin (non-interactive) to use --json")
	}
	return nil
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

func applyChatToolOverrides(res *config.Resolved, allow, deny, disable, allowEnv, denyEnv []string) {
	res.Tools.RunAllowlist = append(res.Tools.RunAllowlist, allow...)
	res.Tools.RunBlocklist = append(res.Tools.RunBlocklist, deny...)
	res.Tools.DisableTools = append(res.Tools.DisableTools, disable...)
	res.Tools.EnvAllowlist = append(res.Tools.EnvAllowlist, allowEnv...)
	res.Tools.EnvBlocklist = append(res.Tools.EnvBlocklist, denyEnv...)
}
