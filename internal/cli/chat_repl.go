package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
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

	noTools, noDefaultAllowlist, plainUI, args := chatFlags(args)
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
	if noDefaultAllowlist {
		res.Tools.RunAllowlistOnly = []string{}
	}
	if len(allowProgram) > 0 {
		res.Tools.RunAllowlist = append(res.Tools.RunAllowlist, allowProgram...)
	}
	if len(denyProgram) > 0 {
		res.Tools.RunBlocklist = append(res.Tools.RunBlocklist, denyProgram...)
	}
	if len(disableTool) > 0 {
		res.Tools.DisableTools = append(res.Tools.DisableTools, disableTool...)
	}
	if len(allowEnvVar) > 0 {
		res.Tools.EnvAllowlist = append(res.Tools.EnvAllowlist, allowEnvVar...)
	}
	if len(denyEnvVar) > 0 {
		res.Tools.EnvBlocklist = append(res.Tools.EnvBlocklist, denyEnvVar...)
	}
	useTools := !noTools
	// Privacy: redact tool args only when explicitly enabled (default off).
	tools.SetRedactToolArgs(res.Privacy.RedactToolArgs)
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
	if err := configureChatWorkspace(sess, wsRoot, useTools, res.TavilyAPIKey, res.Tools, res.Subagents); err != nil {
		return err
	}
	// Create and wire the runtime dispatcher for tool and subagent execution.
	// Shared with interactive session tests so regressions hit the real path.
	cleanup, err := attachSessionDispatcher(sess, wsRoot, res.Model, res.Subagents)
	if err != nil {
		return err
	}
	defer cleanup()
	sess.SessionDir = filepath.Join(wsRoot, ".mivia", "sessions")
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

func chatFlags(args []string) (noTools, noDefaultAllowlist, plainUI bool, rest []string) {
	for _, arg := range args {
		switch arg {
		case "--no-tools":
			noTools = true
		case "--no-default-allowlist":
			noDefaultAllowlist = true
		case "--plain":
			plainUI = true
		default:
			rest = append(rest, arg)
		}
	}
	return noTools, noDefaultAllowlist, plainUI, rest
}

func configureChatWorkspace(sess *chat.Session, root string, useTools bool, tavilyKey string, tc config.ToolsConfig, subagentCfg ...config.SubagentConfig) error {
	if !useTools {
		return nil
	}
	ws, err := workspace.Open(root)
	if err != nil {
		return fmt.Errorf("workspace: %w", err)
	}
	opts := tools.DefaultOptions{
		Workspace:    ws,
		TavilyAPIKey: tavilyKey,
		RunAllowlist: tc.RunAllowlist,
		RunAllowlistOnly: tc.RunAllowlistOnly,
		RunBlocklist: tc.RunBlocklist,
		DisableTools: tc.DisableTools,
		EnvAllowlist: tc.EnvAllowlist,
		EnvAllowlistOnly: tc.EnvAllowlistOnly,
		EnvBlocklist: tc.EnvBlocklist,
		RunTimeoutSec: tc.RunTimeoutSec,
		MaxReadBytes: tc.MaxReadBytes,
		MaxWriteKB: tc.MaxWriteKB,
		MaxOutputBytes: tc.MaxOutputBytes,
		MaxListDirEntries: tc.MaxListDirEntries,
		// RedactToolArgs is NOT plumbed here — the single source of truth
		// is the package atomic set by tools.SetRedactToolArgs at line 40.
		SecretPathPatterns: tc.SecretPathPatterns,
		SecretPathExceptions: tc.SecretPathExceptions,
	}
	sess.Tools = tools.NewDefaultRegistry(opts)
	seedCfg := config.SubagentConfig{}
	if len(subagentCfg) > 0 {
		seedCfg = subagentCfg[0]
	}
	if _, created, err := ensureAgentPromptFile(root, seedCfg); err != nil {
		fmt.Fprintf(os.Stderr, "warning: seed agent prompt: %v\n", err)
	} else if created {
		fmt.Fprintf(os.Stderr, "(created .ai/agent-prompt.md — agent can self-update this file)\n")
	}
	return nil
}

// attachSessionDispatcher loads workspace skills and wires NewSessionDispatcher
// onto the session — the same path interactive runChat uses. Returns a cleanup
// func that closes the dispatcher (safe no-op when tools are off).
func attachSessionDispatcher(sess *chat.Session, root, model string, cfg config.SubagentConfig) (func(), error) {
	if sess == nil || sess.Tools == nil {
		return func() {}, nil
	}
	if sess.Completer == nil {
		return nil, fmt.Errorf("dispatcher: nil completer")
	}
	skillReg, err := skills.LoadMarkdown(filepath.Join(root, ".ai", "skills"), sess.Completer, model)
	if err != nil {
		return nil, fmt.Errorf("load skills: %w", err)
	}
	dispatcher, err := NewSessionDispatcher(sess.Tools, sess.Completer, model, cfg, skillReg)
	if err != nil {
		return nil, fmt.Errorf("dispatcher: %w", err)
	}
	sess.Dispatcher = dispatcher
	return func() { dispatcher.Close() }, nil
}

func repl(sess *chat.Session, res *config.Resolved, toolsOn bool) error {
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
	fmt.Fprintf(os.Stderr, "mivia %s  provider=%s model=%s\n", mode, sess.Completer.Name(), sess.Model)
	if toolsOn {
		fmt.Fprintln(os.Stderr, "Tools on. /tools /workspace /help — Ctrl-C cancel or exit at prompt.")
	} else {
		fmt.Fprintln(os.Stderr, "Tools off (--no-tools). /help — Ctrl-C cancel or exit at prompt.")
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
	fmt.Fprintf(os.Stderr, "  (~%d tokens in history)\n", provider.MessagesTokens(sess.Messages))
	_, err := sess.SendUser(ctx, line, os.Stdout)
	close(done)
	cancel()
	fmt.Fprintln(os.Stdout)
	if ctx.Err() != nil {
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
