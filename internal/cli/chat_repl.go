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
	useTools := !noTools
	if strings.TrimSpace(res.SystemPrompt) == "" {
		if useTools {
			res.SystemPrompt = loadAgentPrompt(workspacePath)
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
	if err := configureChatWorkspace(sess, wsRoot, useTools); err != nil {
		return err
	}
	sess.SessionDir = filepath.Join(wsRoot, ".mivia", "sessions")
	if err := os.MkdirAll(sess.SessionDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "warning: couldn't create session dir: %v\n", err)
	}
	if prompt != "" {
		return oneShot(sess, prompt, useTools, res)
	}
	if plainUI || !term.IsTerminal(int(os.Stdin.Fd())) || strings.EqualFold(os.Getenv("TERM"), "dumb") {
		return repl(sess, res, useTools)
	}
	return runTUI(sess, res, useTools)
}

func chatFlags(args []string) (noTools, plainUI bool, rest []string) {
	for _, arg := range args {
		switch arg {
		case "--no-tools":
			noTools = true
		case "--plain":
			plainUI = true
		default:
			rest = append(rest, arg)
		}
	}
	return noTools, plainUI, rest
}

func configureChatWorkspace(sess *chat.Session, root string, useTools bool) error {
	if !useTools {
		return nil
	}
	ws, err := workspace.Open(root)
	if err != nil {
		return fmt.Errorf("workspace: %w", err)
	}
	sess.Tools = tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})
	if _, created, err := ensureAgentPromptFile(root); err != nil {
		fmt.Fprintf(os.Stderr, "warning: seed agent prompt: %v\n", err)
	} else if created {
		fmt.Fprintf(os.Stderr, "(created .ai/agent-prompt.md — agent can self-update this file)\n")
	}
	return nil
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
	if err := sess.SaveLast(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: auto-save: %v\n", err)
	}
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
