package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/redact"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
	"golang.org/x/term"
)

func runChat(args []string) error {
	prompt, args, _ := flagValue(args, "-p", "--prompt")
	providerName, args, _ := flagValue(args, "--provider")
	model, args, _ := flagValue(args, "--model")
	cfgPath, args, _ := flagValue(args, "--config")
	workspacePath, args, _ := flagValue(args, "--workspace")

	// Phase 5: repeatable value flags
	allowProgram, args, _ := flagVar(args, "--allow-program")
	denyProgram, args, _ := flagVar(args, "--deny-program")
	disableTool, args, _ := flagVar(args, "--disable-tool")
	allowEnvVar, args, _ := flagVar(args, "--allow-env-var")
	denyEnvVar, args, _ := flagVar(args, "--deny-env-var")

	noTools, plainUI, args := chatFlags(args)
	if len(args) > 0 {
		return fmt.Errorf("chat: unexpected arguments: %v", args)
	}
	res, err := config.Load(config.LoadOptions{ConfigPath: cfgPath, ProviderOverride: providerName, ModelOverride: model, AllowMissingConfig: true})
	if err != nil {
		return err
	}
	if !res.APIKeySet {
		return fmt.Errorf("missing API key: set %s in environment or env file (see mivia doctor)", res.APIKeyEnv)
	}
	applyChatToolOverrides(res, allowProgram, denyProgram, disableTool, allowEnvVar, denyEnvVar)
	useTools := !noTools
	// Privacy: redact tool args only when explicitly enabled (default off).
	// Check BOTH [privacy] and [tools] sections so either TOML path works.
	tools.SetRedactToolArgs(res.Privacy.RedactToolArgs || res.Tools.RedactToolArgs)
	// Install the workspace redaction policy for every site that emits
	// operator-visible text. Nil when nothing is configured, which redacts
	// nothing — see .mivia/rules/10-security-privacy.md.
	redact.SetPolicy(res.RedactionPolicy)
	if strings.TrimSpace(res.SystemPrompt) == "" {
		if useTools {
			res.SystemPrompt = loadAgentPrompt(workspacePath, res.Subagents)
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
	wsRoot := workspacePath
	if wsRoot == "" {
		wsRoot = "."
	}
	if err := configureChatWorkspace(sess, wsRoot, useTools, res.TavilyAPIKey, res.Tools); err != nil {
		return err
	}
	// Create and wire the runtime dispatcher for tool and subagent execution.
	// Shared with interactive session tests so regressions hit the real path.
	cleanup, err := attachSessionDispatcher(sess, wsRoot, res.Model, res.Subagents)
	if err != nil {
		return err
	}
	defer cleanup()
	sess.SessionDir = workspace.SessionsDir(wsRoot)
	if err := os.MkdirAll(sess.SessionDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "warning: couldn't create session dir: %v\n", err)
	}

	// Wire SaveManager for auto-save lifecycle (turn snapshots, exit pruning).
	store, err := chat.NewFileSessionStore(sess.SessionDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: couldn't open session store: %v\n", err)
	} else {
		mgr := chat.NewSaveManager(store, res.Model, comp.Name())
		sess.SetSessionStore(store, mgr)
	}

	if prompt != "" {
		return oneShot(sess, prompt, useTools, res)
	}
	if plainUI || !term.IsTerminal(int(os.Stdin.Fd())) || strings.EqualFold(os.Getenv("TERM"), "dumb") {
		return repl(sess, res, useTools)
	}
	return runTUI(sess, res, useTools)
}

func applyChatToolOverrides(res *config.Resolved, allow, deny, disable, allowEnv, denyEnv []string) {
	res.Tools.RunAllowlist = append(res.Tools.RunAllowlist, allow...)
	res.Tools.RunBlocklist = append(res.Tools.RunBlocklist, deny...)
	res.Tools.DisableTools = append(res.Tools.DisableTools, disable...)
	res.Tools.EnvAllowlist = append(res.Tools.EnvAllowlist, allowEnv...)
	res.Tools.EnvBlocklist = append(res.Tools.EnvBlocklist, denyEnv...)
}
