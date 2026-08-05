package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/redact"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/vcs"
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
	expectedWorktreeInstance                                                                          contextstate.WorktreeInstance
	// staleBypass records that the removed --bypass-hook-trust flag was passed,
	// so the session can say the flag no longer does anything.
	staleBypass                            bool
	allowProgram, denyProgram, disableTool []string
	allowEnvVar, denyEnvVar                []string
	noTools, plainUI                       bool
}

func runChat(args []string) error {
	invocation, err := parseChatInvocation(args)
	if err != nil {
		return err
	}
	res, err := config.Load(config.LoadOptions{
		ConfigPath: invocation.configPath, ProviderOverride: invocation.provider,
		ModelOverride: invocation.model, AllowMissingConfig: true,
	})
	if err != nil {
		return err
	}
	return runConfiguredChat(invocation, res)
}

func parseChatInvocation(args []string) (chatInvocation, error) {
	var invocation chatInvocation
	invocation.prompt, args, _ = flagValue(args, "-p", "--prompt")
	invocation.provider, args, _ = flagValue(args, "--provider")
	invocation.model, args, _ = flagValue(args, "--model")
	invocation.configPath, args, _ = flagValue(args, "--config")
	invocation.workspacePath, args, _ = flagValue(args, "--workspace")
	invocation.agent, args, _ = flagValue(args, "--agent")
	invocation.allowProgram, args, _ = flagVar(args, "--allow-program")
	invocation.denyProgram, args, _ = flagVar(args, "--deny-program")
	invocation.disableTool, args, _ = flagVar(args, "--disable-tool")
	invocation.allowEnvVar, args, _ = flagVar(args, "--allow-env-var")
	invocation.denyEnvVar, args, _ = flagVar(args, "--deny-env-var")
	invocation.noTools, invocation.plainUI, invocation.staleBypass, args = chatFlags(args)
	if len(args) > 0 {
		return chatInvocation{}, fmt.Errorf("chat: unexpected arguments: %v", args)
	}
	return invocation, nil
}

var runConfiguredChatOnceImpl = runConfiguredChatOnce
var loadConfigForRestart = config.Load
var classifyMissingMarkerForBind = classifyMissingWorktreeMarker

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
		var restart *workspaceRestart
		if !errors.As(err, &restart) {
			return err
		}
		if err := validateWorkspaceRestart(*restart, invocation); err != nil {
			return fmt.Errorf("validate workspace restart: %w", err)
		}
		invocation.workspacePath = restart.dir
		invocation.resumeSessionName = restart.resumeSessionName
		invocation.expectedWorktreeInstance = restart.worktreeInstance
		if err := os.Chdir(restart.dir); err != nil {
			return fmt.Errorf("enter restarted workspace: %w", err)
		}
		reloaded, err := loadConfigForRestart(config.LoadOptions{
			ConfigPath:         invocation.configPath,
			ProviderOverride:   invocation.provider,
			ModelOverride:      invocation.model,
			AllowMissingConfig: true,
		})
		if err != nil {
			return err
		}
		res = reloaded
	}
}

func validateWorkspaceRestart(restart workspaceRestart, invocation chatInvocation) error {
	if restart.worktreeInstance.IsZero() {
		return nil
	}
	root, err := chatRepositoryRoot(restart.dir)
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
	return validateExpectedWorktreeInstanceInStore(store, root, restart.dir, restart.worktreeInstance)
}

func runConfiguredChatOnce(invocation chatInvocation, res *config.Resolved) error {
	if !res.APIKeySet {
		return fmt.Errorf("missing API key: set %s in environment or env file (see mivia doctor)", res.APIKeyEnv)
	}
	applyChatToolOverrides(res, invocation.allowProgram, invocation.denyProgram, invocation.disableTool, invocation.allowEnvVar, invocation.denyEnvVar)
	useTools := !invocation.noTools
	applyPrivacyPolicy(res)
	logEffectiveLimitsOnce(os.Stderr, res)
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
	applyWorkspacePromptGate(res, agentState.Global)
	releaseHooks, err := installHookSession(wsRoot, invocation.staleBypass)
	if err != nil {
		return err
	}
	defer releaseHooks()
	if strings.TrimSpace(res.SystemPrompt) == "" {
		if useTools {
			res.SystemPrompt = loadAgentPrompt(invocation.workspacePath, res.Subagents)
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
	installSessionIdentity(sess, agentState)
	agentState.BaselinePrompt = sess.SystemPrompt
	agentState.BaselineMaxSteps = sess.MaxStepsValue()
	agentState.BaselineCaptured = true
	setActiveSessionCaller(runtime.Caller{SessionID: sess.SessionID})
	if err := configureChatWorkspace(sess, wsRoot, useTools, res.TavilyAPIKey, res.Tools); err != nil {
		return err
	}
	applySelectedAgentPrompt(sess, res, agentState.Selected)
	contextStore, err := setupChatSessionContext(sess, wsRoot, invocation, res)
	if err != nil {
		return err
	}
	defer contextStore.Close()
	// Capture pointer so /agent and model-switch rebuilds see updates.
	sess.SetBindingFactory(chatBindingFactory(sess, res, wsRoot, agentState))
	contextWiring := contextDispatcherFor(sess, res.Subagents)
	cleanup, err := attachSessionDispatcher(sess, wsRoot, res.Model, res.Subagents, agentState, skillReg, sessionRouting{
		Catalog: res.ModelCatalog(), CompleterFactory: newProviderCompleterFactory(res),
		Context: contextWiring, Resolved: res,
	})
	if err != nil {
		return err
	}
	defer cleanup()
	if invocation.prompt != "" {
		return oneShot(sess, invocation.prompt, useTools, res)
	}
	// Classic REPL /agent uses package state; TUI stores agentState on the model.
	classicAgentState = agentState
	defer func() { classicAgentState = nil }()
	if invocation.plainUI || !term.IsTerminal(int(os.Stdin.Fd())) || strings.EqualFold(os.Getenv("TERM"), "dumb") {
		return repl(sess, res, useTools, agentState)
	}
	return runTUI(sess, res, useTools, agentState, invocation.resumeSessionName)
}

func chatRepositoryRoot(path string) (string, error) {
	if path == "" {
		path = "."
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return vcs.MainRepoRoot(abs)
}

func setupChatSessionContext(sess *chat.Session, workspaceRoot string, invocation chatInvocation, res *config.Resolved) (*storage.SQLite, error) {
	repositoryRoot, err := chatRepositoryRoot(workspaceRoot)
	if err == nil {
		if err := bindManagedWorktreeSessionExpected(sess, repositoryRoot, workspaceRoot, invocation.repositorySessionStorePath, invocation.expectedWorktreeInstance); err != nil {
			return nil, err
		}
	}
	if err != nil || invocation.repositorySessionStorePath == "" {
		return setupSessionContext(sess, workspaceRoot, res)
	}
	return setupRepositorySessionContext(sess, repositoryRoot, invocation.repositorySessionStorePath, res)
}

func bindManagedWorktreeSession(sess *chat.Session, repositoryRoot, workspaceRoot, storePath string) error {
	return bindManagedWorktreeSessionExpected(sess, repositoryRoot, workspaceRoot, storePath, contextstate.WorktreeInstance{})
}

func bindManagedWorktreeSessionExpected(sess *chat.Session, repositoryRoot, workspaceRoot, storePath string, expected contextstate.WorktreeInstance) error {
	name, err := vcs.CurrentWorktreeName(context.Background(), workspaceRoot)
	if err != nil {
		return err
	}
	if name == "" {
		if !expected.IsZero() {
			return contextstate.ErrWorktreeDeleted
		}
		return nil
	}
	if !expected.IsZero() && expected.Worktree != name {
		return contextstate.ErrWorktreeDeleted
	}
	worktree, err := vcs.Resolve(context.Background(), repositoryRoot, name)
	if err != nil {
		return err
	}
	if worktree == nil {
		return fmt.Errorf("managed worktree %q is not available", name)
	}
	canonicalPath, err := canonicalMarkerRoot(worktree.Path)
	if err != nil {
		return err
	}
	if storePath == "" {
		storePath, err = repositorySessionStorePath(repositoryRoot, chatInvocation{}, &config.Resolved{})
		if err != nil {
			return err
		}
	}
	store, err := openContextStorePath(storePath)
	if err != nil {
		return err
	}
	defer store.Close()
	principal, _ := worktreeRoutePrincipal(repositoryRoot)
	instance, markerErr := readWorktreeMarker(worktree.Path)
	if errors.Is(markerErr, os.ErrNotExist) {
		if !expected.IsZero() {
			return contextstate.ErrWorktreeDeleted
		}
		info, legacy, err := classifyMissingMarkerForBind(store, principal, name, canonicalPath)
		if err != nil {
			return err
		}
		if !info.Instance.IsZero() {
			return fmt.Errorf("managed worktree %q has state %q but no marker: %w", name, info.State, contextstate.ErrWorktreeDeleted)
		}
		if legacy {
			return fmt.Errorf("worktree %q requires adoption; run mivia worktree adopt %s", name, name)
		}
		return nil
	}
	if markerErr != nil {
		return fmt.Errorf("read worktree session marker: %w", markerErr)
	}
	if instance.Worktree != name {
		return fmt.Errorf("worktree session marker does not match %q", name)
	}
	if !expected.IsZero() && instance != expected {
		return contextstate.ErrWorktreeDeleted
	}
	if err := store.ValidateActiveWorktreeInstance(context.Background(), principal, instance, canonicalPath); err != nil {
		return fmt.Errorf("validate worktree session binding: %w", err)
	}
	sessionDir, err := canonicalMarkerRoot(workspaceRoot)
	if err != nil {
		return err
	}
	return sess.SetContextWorktreeBindingAt(instance, canonicalPath, sessionDir)
}

func repositorySessionStorePath(root string, invocation chatInvocation, _ *config.Resolved) (string, error) {
	configPath, found := repositoryConfigPath(root, invocation)
	if !found {
		return config.DefaultStorePathForWorkspace(root), nil
	}
	resolved, err := config.Load(config.LoadOptions{ConfigPath: configPath, AllowMissingConfig: true})
	if err != nil {
		return "", err
	}
	if !resolved.StorePathSet {
		return config.DefaultStorePathForWorkspace(root), nil
	}
	path := config.ExpandPath(resolved.Subagents.StorePath)
	if filepath.IsAbs(path) {
		return path, nil
	}
	return filepath.Join(root, path), nil
}

func repositoryConfigPath(root string, invocation chatInvocation) (string, bool) {
	configPath := invocation.configPath
	if configPath == "" {
		configPath = os.Getenv("MIVIA_CONFIG")
	}
	if configPath != "" {
		path := config.ExpandPath(configPath)
		if !filepath.IsAbs(path) {
			absolute, err := filepath.Abs(path)
			if err != nil {
				return "", false
			}
			path = absolute
		}
		if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
			return "", false
		}
		return path, true
	}
	return config.FirstExisting([]string{
		filepath.Join(root, ".mivia", "mivia.toml"),
		config.UserConfigPath(),
	})
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

// loadChatSkills loads session skills under the user gate before agent resolve
// so skill-name collisions fail closed. Project skills load only when the gate
// is on, so they cannot shadow then erase user skills.
func loadChatSkills(wsRoot string) (*skills.Registry, error) {
	globalPreview, err := config.LoadAgentsGlobal(wsRoot)
	if err != nil {
		return nil, err
	}
	skillReg, skillWarnings, err := loadSessionSkills(wsRoot, globalPreview.LoadWorkspaceConfig)
	if err != nil {
		return nil, fmt.Errorf("load skills: %w", err)
	}
	warnSkillLoad(skillWarnings)
	return skillReg, nil
}

// prepareAgentSession loads and optionally selects a named agent definition.
func prepareAgentSession(wsRoot, agentFlag string, skillReg *skills.Registry) (*agentSessionState, error) {
	loaded, err := loadAgentDefinitions(wsRoot, agentFlag, skillReg)
	if err != nil {
		return nil, err
	}
	warnAgentLoad(loaded.Warnings)
	return &agentSessionState{
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
