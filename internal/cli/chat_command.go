package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/redact"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
	"golang.org/x/term"
)

// applyPrivacyPolicy installs the process-wide privacy settings.
//
// Tool-argument redaction is opt-in and read from BOTH [privacy] and [tools]
// so either TOML path works. The redaction policy is nil when the workspace
// configured no patterns, which redacts nothing — see rule 10.
func applyPrivacyPolicy(res *config.Resolved) {
	tools.SetRedactToolArgs(res.Privacy.RedactToolArgs || res.Tools.RedactToolArgs)
	redact.SetPolicy(res.RedactionPolicy)
}

type chatInvocation struct {
	prompt, provider, model, configPath, workspacePath string
	agent                                              string
	allowProgram, denyProgram, disableTool             []string
	allowEnvVar, denyEnvVar                            []string
	noTools, plainUI                                   bool
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
	invocation.noTools, invocation.plainUI, args = chatFlags(args)
	if len(args) > 0 {
		return chatInvocation{}, fmt.Errorf("chat: unexpected arguments: %v", args)
	}
	return invocation, nil
}

func runConfiguredChat(invocation chatInvocation, res *config.Resolved) error {
	if !res.APIKeySet {
		return fmt.Errorf("missing API key: set %s in environment or env file (see mivia doctor)", res.APIKeyEnv)
	}
	applyChatToolOverrides(res, invocation.allowProgram, invocation.denyProgram, invocation.disableTool, invocation.allowEnvVar, invocation.denyEnvVar)
	useTools := !invocation.noTools
	applyPrivacyPolicy(res)
	wsRoot := invocation.workspacePath
	if wsRoot == "" {
		wsRoot = "."
	}

	// Load skills before agent validation so skill-name collisions fail closed.
	// Pure-chat (--no-tools) still loads skills for collision checks but a
	// malformed workspace skill must not make startup fatal (LoadMarkdownSources
	// already warns and continues).
	skillReg, skillWarnings, err := loadSessionSkills(wsRoot)
	if err != nil {
		return fmt.Errorf("load skills: %w", err)
	}
	warnSkillLoad(skillWarnings)

	agentState, err := prepareAgentSession(wsRoot, invocation.agent, skillReg)
	if err != nil {
		return err
	}
	applyWorkspacePromptGate(res, agentState.Global)
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
	setActiveSessionCaller(runtime.Caller{SessionID: sess.SessionID})
	if err := configureChatWorkspace(sess, wsRoot, useTools, res.TavilyAPIKey, res.Tools); err != nil {
		return err
	}
	applySelectedAgentPrompt(sess, res, agentState.Selected)
	// Capture pointer so /agent and model-switch rebuilds see updates.
	sess.SetBindingFactory(func(providerName, model string) (chat.ModelBinding, error) {
		return buildModelBinding(sess, res, wsRoot, providerName, model, agentState.context())
	})
	cleanup, err := attachSessionDispatcher(sess, wsRoot, res.Model, res.Subagents, agentState, skillReg)
	if err != nil {
		return err
	}
	defer cleanup()
	sess.SessionDir = workspace.SessionsDir(wsRoot)
	if err := os.MkdirAll(sess.SessionDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "warning: couldn't create session dir: %v\n", err)
	}
	store, err := chat.NewFileSessionStore(sess.SessionDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: couldn't open session store: %v\n", err)
	} else {
		mgr := chat.NewSaveManager(store, res.Model, comp.Name())
		sess.SetSessionStore(store, mgr)
	}
	if invocation.prompt != "" {
		return oneShot(sess, invocation.prompt, useTools, res)
	}
	// Classic REPL /agent uses package state; TUI stores agentState on the model.
	classicAgentState = agentState
	defer func() { classicAgentState = nil }()
	if invocation.plainUI || !term.IsTerminal(int(os.Stdin.Fd())) || strings.EqualFold(os.Getenv("TERM"), "dumb") {
		return repl(sess, res, useTools, agentState)
	}
	return runTUI(sess, res, useTools, agentState)
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
