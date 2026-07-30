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
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

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
		// RedactToolArgs is NOT plumbed here — the single source of truth
		// is the package atomic set by tools.SetRedactToolArgs at line 40.
		SecretPathPatterns:   tc.SecretPathPatterns,
		SecretPathExceptions: tc.SecretPathExceptions,
	}
	sess.Tools = tools.NewDefaultRegistry(opts)
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
	skillReg, err := skills.LoadMarkdown(workspace.SkillsDir(root), sess.Completer, model)
	if err != nil {
		return nil, fmt.Errorf("load skills: %w", err)
	}
	// sess.MaxToolResultChars carries [tools] max_tool_result_bytes, so nested
	// sub-agent loops share the interactive loop's ceiling (0 = uncapped).
	dispatcher, err := NewSessionDispatcher(sess.Tools, sess.Completer, model, cfg, sess.MaxToolResultChars, skillReg)
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
