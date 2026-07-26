// Package cli implements mivia command handlers.
package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

func runChat(args []string) error {
	prompt, args, _ := flagValue(args, "-p", "--prompt")
	providerName, args, _ := flagValue(args, "--provider")
	model, args, _ := flagValue(args, "--model")
	cfgPath, args, _ := flagValue(args, "--config")
	workspacePath, args, _ := flagValue(args, "--workspace")
	noTools := false
	var rest []string
	for _, a := range args {
		if a == "--no-tools" {
			noTools = true
			continue
		}
		rest = append(rest, a)
	}
	args = rest
	if len(args) > 0 {
		return fmt.Errorf("chat: unexpected arguments: %v", args)
	}

	res, err := config.Load(config.LoadOptions{
		ConfigPath:         cfgPath,
		ProviderOverride:   providerName,
		ModelOverride:      model,
		AllowMissingConfig: true,
	})
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

	// Determine workspace root for tools and session persistence.
	wsRoot := workspacePath
	if wsRoot == "" {
		wsRoot = "."
	}

	if useTools {
		ws, err := workspace.Open(wsRoot)
		if err != nil {
			return fmt.Errorf("workspace: %w", err)
		}
		sess.Tools = tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})
		// Agent events set later in repl() when we have a terminal/renderer.

		// Seed .ai/agent-prompt.md if missing so the agent can self-update.
		if _, created, err := ensureAgentPromptFile(wsRoot); err != nil {
			fmt.Fprintf(os.Stderr, "warning: seed agent prompt: %v\n", err)
		} else if created {
			fmt.Fprintf(os.Stderr, "(created .ai/agent-prompt.md — agent can self-update this file)\n")
		}
	}

	// Set up session persistence directory under the workspace.
	sessionDir := filepath.Join(wsRoot, ".mivia", "sessions")
	sess.SessionDir = sessionDir
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "warning: couldn't create session dir: %v\n", err)
	}

	if prompt != "" {
		return oneShot(sess, prompt, useTools, res)
	}
	// Use Bubble Tea TUI when terminal is available.
	return runTUI(sess, res, useTools)
}

// makeAgentUIWithRenderer returns an OnEvent handler that formats via a ChatRenderer.
func makeAgentUIWithRenderer(r *ChatRenderer) func(agent.Event) {
	return func(e agent.Event) {
		switch e.Kind {
		case agent.EventStep:
			if e.Detail != "" {
				r.PrintStep(e.Detail)
			}
		case agent.EventToolStart:
			r.PrintToolStart(e.Name, e.Detail)
		case agent.EventToolEnd:
			r.PrintToolEnd(e.Name, e.Detail)
		case agent.EventToolParallel:
			if e.Detail != "" {
				r.PrintParallel(e.Detail)
			}
		case agent.EventAssistant:
			// Printed by FinalWriter; no need to duplicate.
		case agent.EventPrune:
			if e.Detail != "" {
				r.PrintPrune(e.Detail)
			}
		}
	}
}

func oneShot(sess *chat.Session, prompt string, toolsOn bool, res *config.Resolved) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	mode := "chat"
	if toolsOn {
		mode = "agent"
	}
	fmt.Fprintf(os.Stderr, "mivia %s  provider=%s model=%s\n", mode, res.ProviderName, sess.Model)
	_, err := sess.SendUser(ctx, prompt, os.Stdout)
	if err != nil {
		if ctx.Err() != nil {
			fmt.Fprintln(os.Stderr, "\n(cancelled)")
			return nil
		}
		return err
	}
	fmt.Fprintln(os.Stdout)
	return nil
}

func repl(sess *chat.Session, res *config.Resolved, toolsOn bool) error {
	mode := "chat"
	if toolsOn {
		mode = "agent"
	}
	fmt.Fprintf(os.Stderr, "mivia %s  provider=%s model=%s\n", mode, res.ProviderName, sess.Model)
	if toolsOn {
		fmt.Fprintf(os.Stderr, "Tools on. /tools /workspace /help — Ctrl-C cancel or exit at prompt.\n")
	} else {
		fmt.Fprintf(os.Stderr, "Tools off (--no-tools). /help — Ctrl-C cancel or exit at prompt.\n")
	}

	// Auto-save session on graceful exit (Ctrl-D, /exit, /quit).
	defer func() {
		if err := sess.SaveLast(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: auto-save: %v\n", err)
		}
	}()

	// Initialize terminal for raw-mode input.
	term, err := NewTerminal()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: not a terminal (%v), falling back to line mode\n", err)
		return replLineMode(sess, res, toolsOn)
	}
	defer term.Close()

	modelShort := shortenModel(sess.Model)
	input := NewInputBuffer(" " + modelShort + " > ")
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	defer signal.Stop(sigCh)

	// Create chat renderer bound to the terminal (stderr in REPL mode).
	renderer := NewChatRenderer(term, sess.Model)

	// Wire agent events to the renderer.
	if toolsOn {
		sess.OnAgentEvent = makeAgentUIWithRenderer(renderer)
	}

	// Auto-load previous session for continuity across rebuilds.
	if sess.HasAutoSave() {
		if err := sess.Load(chat.AutoSaveName); err == nil && sess.UserTurns() > 0 {
			renderer.PrintDim("Restored previous session (%d messages, %d turns)", len(sess.Messages), sess.UserTurns())
			renderer.RenderHistory(sess.Messages)
		}
	}

	// Main REPL loop.
	var pasteBuf string
	inPaste := false
	for {
		// Render the input line with multi-line wrapping support.
		input.RenderInPlace(term)

		// Read a key.
		key, err := term.ReadKey()
		if err != nil {
			return err
		}

		// Handle bracketed paste start sequence: \033[200~
		if !inPaste && key == "\033" || strings.HasPrefix(key, "\033[200") || key == "\033[200~" {
			// Read remaining bytes of potential paste sequence.
			extras := make([]byte, 0, 8)
			if strings.HasPrefix(key, "\033[200~") {
				// Full paste start in one read.
				inPaste = true
				pasteBuf = ""
				continue
			}
			if len(key) > 1 {
				// Already have some bytes after \033.
				extras = []byte(key[1:])
			}
			for !inPaste {
				b := make([]byte, 1)
				n, _ := os.Stdin.Read(b)
				if n == 0 {
					break
				}
				extras = append(extras, b[0])
				seq := "\033" + string(extras)
				if seq == "\033[200~" {
					inPaste = true
					pasteBuf = ""
					break
				}
				if seq == "\033[201~" {
					break
				}
				// Check if it's a complete escape sequence.
				last := extras[len(extras)-1]
				if (last >= 'A' && last <= 'Z') || last == '~' {
					// Known sequence, handle below.
					break
				}
				if len(extras) > 8 {
					break
				}
			}
			if !inPaste {
				seq := "\033" + string(extras)
				switch {
				case seq == "\033[A":
					input.PrevHistory()
				case seq == "\033[B":
					input.NextHistory()
				case seq == "\033[C":
					input.MoveRight()
				case seq == "\033[D":
					input.MoveLeft()
				case seq == "\033[H" || seq == "\033[1~":
					input.MoveHome()
				case seq == "\033[F" || seq == "\033[4~":
					input.MoveEnd()
				case seq == "\033[3~":
					input.Delete()
				case seq == "\033[Z":
				default:
					ShowHelpDialog(term)
				}
			}
			continue
		}

		// Handle bracketed paste content.
		if inPaste {
			// Check if key contains the end sequence \033[201~
			if idx := strings.Index(key, "\033[201~"); idx >= 0 {
				// Text before end sequence.
				prefix := key[:idx]
				for _, r := range prefix {
					if r == '\r' {
						r = '\n'
					}
					if r >= 32 || r == '\n' || r == '\t' {
						pasteBuf += string(r)
					}
				}
				// Insert all accumulated paste content.
				for _, r := range pasteBuf {
					if r == '\r' {
						r = '\n'
					}
					if r >= 32 || r == '\n' || r == '\t' {
						input.Insert(r)
					}
				}
				inPaste = false
				pasteBuf = ""
				continue
			}
			// Also check for partial end sequence start: \033[2 or \033[20 etc.
			if strings.Contains(key, "\033") {
				// Split key at \033 boundary.
				parts := strings.SplitN(key, "\033", 2)
				if len(parts) == 2 {
					// Everything before \033 is paste content.
					for _, r := range parts[0] {
						if r == '\r' {
							r = '\n'
						}
						if r >= 32 || r == '\n' || r == '\t' {
							pasteBuf += string(r)
						}
					}
					// Read the rest to see if it completes \033[201~
					extras := []byte(parts[1])
					for {
						seq := "\033" + string(extras)
						if seq == "\033[201~" {
							for _, r := range pasteBuf {
								if r == '\r' {
									r = '\n'
								}
								if r >= 32 || r == '\n' || r == '\t' {
									input.Insert(r)
								}
							}
							inPaste = false
							pasteBuf = ""
							break
						}
						b := make([]byte, 1)
						n, _ := os.Stdin.Read(b)
						if n == 0 {
							break
						}
						extras = append(extras, b[0])
						if len(extras) > 8 {
							// Not matching; treat \033 and extras as paste content.
							pasteBuf += "\033" + string(extras)
							break
						}
					}
					continue
				}
			}
			// Accumulate pasted characters.
			for _, r := range key {
				if r == '\r' {
					r = '\n'
				}
				if r >= 32 || r == '\n' || r == '\t' {
					pasteBuf += string(r)
				}
			}
			continue
		}

		// Handle key.
		switch {
		case key == "\r" || key == "\n":
			// Enter — commit input.
			line := input.Commit()
			if line == "" {
				continue
			}
			if err := processLineChat(line, sess, res, toolsOn, term, renderer, input, modelShort); err != nil {
				return err
			}
			if line == "exit" || line == "quit" {
				return nil
			}
			// Update modelShort and renderer in case /model changed it.
			modelShort = shortenModel(sess.Model)
			input.SetPrompt(" " + modelShort + " > ")
			renderer = NewChatRenderer(term, sess.Model)

		case key == "\x7f" || key == "\b":
			input.Backspace()

		case key == "\x01":
			input.MoveHome()
		case key == "\x05":
			input.MoveEnd()
		case key == "\x15":
			input.KillLine()
		case key == "\x17":
			input.KillWord()
		case key == "\x0b":
			input.KillToEnd()
		case key == "\x09":
			handleTab(input)
		case key == "\x04":
			term.ClearLine()
			term.WriteString("\n")
			return nil
		case key == "\x03":
			term.ClearLine()
			term.WriteString("\n")
			return nil
		default:
			if len(key) == 1 && key[0] >= 32 {
				input.Insert(rune(key[0]))
			}
		}
	}
}

// processLineChat handles a committed input line with chat-style formatting.
// All output goes to the terminal (stderr) in REPL mode, not stdout.
func processLineChat(line string, sess *chat.Session, res *config.Resolved, toolsOn bool, term *Terminal, renderer *ChatRenderer, input *InputBuffer, modelShort string) error {
	// Transform /search <query> to a natural language request that routes
	// through the AI model — the model calls the search tool, gets results,
	// and returns a synthesized answer with proper formatting.
	if strings.HasPrefix(line, "/search") {
		query := strings.TrimSpace(strings.TrimPrefix(line, "/search"))
		if query == "" {
			renderer.PrintInfo("usage: /search <query> — searches the web and returns AI-synthesized results")
			return nil
		}
		line = "search the web for: " + query
		// Fall through to the AI path below — don't handle as a slash command.
	}

	// Check for other slash commands.
	if strings.HasPrefix(line, "/") {
		if handled, exit, herr := handleSlash(line, sess, res, toolsOn, term); handled {
			if herr != nil {
				renderer.PrintError(herr.Error())
			}
			if exit {
				return nil
			}
			return nil
		}
	}
	if line == "exit" || line == "quit" {
		return nil
	}

	// Set up context with signal cancellation.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	defer signal.Stop(sigCh)

	done := make(chan struct{})
	go func() {
		select {
		case <-sigCh:
			cancel()
		case <-done:
		}
	}()

	// --- Chat-style formatting ---
	// Print user message with header. The user text is already committed from
	// the input buffer — no need to clear anything since the prompt was drawn
	// at the bottom and we write to stderr which scrolls naturally.
	renderer.PrintUser(line)

	// Print assistant header before model response starts.
	renderer.PrintAssistantHeader()

	// Send to model — wrap term with MarkdownWriter so streaming markdown
	// is rendered with ANSI formatting.
	mw := NewMarkdownWriter(term)
	_, err := sess.SendUser(ctx, line, mw)
	mw.Flush()

	// After model output finishes, add a trailing newline and redraw prompt.
	term.WriteString("\n")
	input.RenderInPlace(term)

	close(done)
	if err != nil {
		if ctx.Err() != nil {
			renderer.PrintInfo("(cancelled — still in session; /exit to quit)")
			select {
			case <-sigCh:
			default:
			}
			return nil
		}
		renderer.PrintError(err.Error())
		return nil
	}
	return nil
}

// handleSlash handles /commands, with terminal-aware output.
func handleSlash(line string, sess *chat.Session, res *config.Resolved, toolsOn bool, term *Terminal) (handled, exit bool, err error) {
	if !strings.HasPrefix(line, "/") {
		return false, false, nil
	}
	fields := strings.Fields(line)
	cmd := strings.ToLower(fields[0])
	switch cmd {
	case "/exit", "/quit", "/q":
		return true, true, nil
	case "/help", "/h", "/?":
		if term != nil {
			ShowHelpDialog(term)
		} else {
			fmt.Fprint(os.Stderr, slashHelp)
		}
		return true, false, nil
	case "/clear":
		sess.Clear()
		term.WriteString("\n(history cleared)")
		return true, false, nil
	case "/status":
		tokens := provider.MessagesTokens(sess.Messages)
		term.WriteString(fmt.Sprintf(
			"\nprovider=%s model=%s tools=%v turns=%d messages=%d context=%d tokens (est.)",
			sess.Completer.Name(), sess.Model, toolsOn && sess.UseTools, sess.UserTurns(), len(sess.Messages), tokens))
		if sess.MaxContextTokens > 0 {
			pct := 100 * tokens / sess.MaxContextTokens
			term.WriteString(fmt.Sprintf("\ncontext budget=%d tokens (%d%% used)", sess.MaxContextTokens, pct))
		}
		return true, false, nil
	case "/model":
		if len(fields) < 2 {
			term.WriteString(fmt.Sprintf("\ncurrent model=%s\nusage: /model deepseek-v4-flash|deepseek-v4-pro|<name>", sess.Model))
			return true, false, nil
		}
		sess.Model = fields[1]
		term.WriteString(fmt.Sprintf("\n(model set to %s)", sess.Model))
		return true, false, nil
	case "/provider":
		term.WriteString(fmt.Sprintf("\nprovider=%s (restart with --provider to switch)", res.ProviderName))
		return true, false, nil
	case "/tools":
		if sess.Tools == nil {
			term.WriteString("\ntools disabled (--no-tools)")
			return true, false, nil
		}
		for _, t := range sess.Tools.List() {
			term.WriteString(fmt.Sprintf("\n  %s — %s", t.Name(), t.Description()))
		}
		return true, false, nil
	case "/workspace":
		if sess.Tools == nil {
			term.WriteString("\ntools disabled")
			return true, false, nil
		}
		cwd, _ := os.Getwd()
		term.WriteString(fmt.Sprintf("\nworkspace defaults to process cwd unless --workspace set: %s", cwd))
		return true, false, nil
	case "/budget":
		if len(fields) >= 2 {
			var n int
			if _, err := fmt.Sscanf(fields[1], "%d", &n); err != nil || n < 0 {
				term.WriteString(fmt.Sprintf("\ninvalid budget %q; use a positive number", fields[1]))
				return true, false, nil
			}
			if n == 0 {
				n = chat.DefaultMaxContextTokens
			}
			sess.MaxContextTokens = n
			term.WriteString(fmt.Sprintf("\n(context budget set to %d tokens)", n))
			return true, false, nil
		}
		term.WriteString(fmt.Sprintf("\ncontext budget=%d tokens\nusage: /budget <tokens>\n  set to 0 for default (%d)", sess.MaxContextTokens, chat.DefaultMaxContextTokens))
		return true, false, nil
	case "/steps":
		if len(fields) >= 2 {
			var n int
			if _, err := fmt.Sscanf(fields[1], "%d", &n); err != nil || n < 0 {
				term.WriteString(fmt.Sprintf("\ninvalid step limit %q; use a positive number (0 = unlimited)", fields[1]))
				return true, false, nil
			}
			sess.MaxSteps = n
			if n <= 0 {
				term.WriteString("\n(max steps set to unlimited)")
			} else {
				term.WriteString(fmt.Sprintf("\n(max steps set to %d)", n))
			}
			return true, false, nil
		}
		if sess.MaxSteps <= 0 {
			term.WriteString("\nmax steps: unlimited\nusage: /steps <n> (set to 0 for unlimited)")
		} else {
			term.WriteString(fmt.Sprintf("\nmax steps: %d\nusage: /steps <n> (set to 0 for unlimited)", sess.MaxSteps))
		}
		return true, false, nil
	case "/save":
		name := strings.TrimSpace(strings.TrimPrefix(line, cmd))
		name = strings.TrimSpace(strings.TrimPrefix(name, "/save"))
		if name == "" {
			term.WriteString("\nusage: /save <name>")
			return true, false, nil
		}
		if err := sess.Save(name); err != nil {
			term.WriteString(fmt.Sprintf("\nsave error: %v", err))
			return true, false, nil
		}
		turns := sess.UserTurns()
		term.WriteString(fmt.Sprintf("\n(session %q saved — %d messages, %d turns)", name, len(sess.Messages), turns))
		return true, false, nil
	case "/load":
		name := strings.TrimSpace(strings.TrimPrefix(line, cmd))
		name = strings.TrimSpace(strings.TrimPrefix(name, "/load"))
		if name == "" {
			term.WriteString("\nusage: /load <name>")
			return true, false, nil
		}
		if err := sess.Load(name); err != nil {
			term.WriteString(fmt.Sprintf("\nload error: %v", err))
			return true, false, nil
		}
		term.WriteString(fmt.Sprintf("\n(session %q loaded — %d messages, %d turns)\n", name, len(sess.Messages), sess.UserTurns()))
		// Render loaded conversation history using term as the writer.
		r := NewChatRenderer(term, sess.Model)
		r.RenderHistory(sess.Messages)
		return true, false, nil
	case "/list":
		sessions, err := sess.ListSessions()
		if err != nil {
			term.WriteString(fmt.Sprintf("\nlist error: %v", err))
			return true, false, nil
		}
		if len(sessions) == 0 {
			term.WriteString("\n(no saved sessions)")
			return true, false, nil
		}
		term.WriteString("\nsaved sessions:")
		for _, si := range sessions {
			ago := time.Since(si.UpdatedAt).Truncate(time.Second)
			marker := ""
			if si.Name == chat.AutoSaveName {
				marker = " [auto]"
			}
			term.WriteString(fmt.Sprintf("\n  %-20s  %3d msgs  %3d turns  ~%6d tok  %s ago%s  (%s)",
				si.Name, si.MessageCount, si.TurnCount, si.TokenCount, ago, marker, si.Model))
		}
		return true, false, nil
	case "/delete":
		name := strings.TrimSpace(strings.TrimPrefix(line, cmd))
		name = strings.TrimSpace(strings.TrimPrefix(name, "/delete"))
		if name == "" {
			term.WriteString("\nusage: /delete <name>")
			return true, false, nil
		}
		if err := sess.DeleteSession(name); err != nil {
			term.WriteString(fmt.Sprintf("\ndelete error: %v", err))
			return true, false, nil
		}
		term.WriteString(fmt.Sprintf("\n(session %q deleted)", name))
		return true, false, nil
	case "/session":
		term.WriteString(fmt.Sprintf("\ncurrent: %d messages, %d turns, ~%d tokens",
			len(sess.Messages), sess.UserTurns(), provider.MessagesTokens(sess.Messages)))
		term.WriteString(fmt.Sprintf("\nsessions dir: %s", sess.SessionDir))
		sessions, listErr := sess.ListSessions()
		if listErr != nil {
			term.WriteString(fmt.Sprintf("\nsaved: (list error: %v)", listErr))
		} else if len(sessions) > 0 {
			term.WriteString(fmt.Sprintf("\nsaved: %d session(s)", len(sessions)))
		} else {
			term.WriteString("\nno saved sessions yet")
		}
		return true, false, nil
	default:
		term.WriteString(fmt.Sprintf("\nunknown command %q (try /help)", cmd))
		return true, false, nil
	}
}

// handleTab performs simple command completion.
func handleTab(input *InputBuffer) {
	current := input.String()
	if !strings.HasPrefix(current, "/") {
		return
	}
	known := []string{
		"/help", "/exit", "/quit", "/clear", "/status",
		"/model", "/provider", "/tools", "/workspace", "/budget",
		"/steps", "/search",
		"/save", "/load", "/delete", "/list", "/session",
	}
	var matches []string
	for _, k := range known {
		if strings.HasPrefix(k, current) {
			matches = append(matches, k)
		}
	}
	if len(matches) == 1 {
		input.SetString(matches[0] + " ")
	} else if len(matches) > 1 {
		prefix := commonPrefix(matches)
		if prefix != current {
			input.SetString(prefix)
		}
	}
}

func commonPrefix(strs []string) string {
	if len(strs) == 0 {
		return ""
	}
	prefix := strs[0]
	for _, s := range strs[1:] {
		for !strings.HasPrefix(s, prefix) {
			prefix = prefix[:len(prefix)-1]
		}
	}
	return prefix
}

func shortenModel(m string) string {
	if len(m) > 24 {
		return m[:21] + "..."
	}
	return m
}

// replLineMode is the fallback when stdin is not a terminal.
func replLineMode(sess *chat.Session, res *config.Resolved, toolsOn bool) error {
	sc := bufio.NewScanner(os.Stdin)
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 1024*1024)

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

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			select {
			case <-sigCh:
				cancel()
			case <-done:
			}
		}()

		before := provider.MessagesTokens(sess.Messages)
		fmt.Fprintf(os.Stderr, "  (~%d tokens in history)\n", before)

		_, err := sess.SendUser(ctx, line, os.Stdout)
		close(done)
		cancel()
		fmt.Fprintln(os.Stdout)

		if err != nil {
			if ctx.Err() != nil {
				fmt.Fprintln(os.Stderr, "(cancelled)")
				select {
				case <-sigCh:
				default:
				}
				continue
			}
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			continue
		}
	}
	return sc.Err()
}

const slashHelp = `commands:
  /help              show this help
  /exit /quit /q     leave
  /clear             clear conversation history
  /status            provider, model, tools, turns, context tokens
  /model <name>      set model (e.g. deepseek-v4-pro)
  /tools             list tools
  /workspace         show workspace hint
  /provider          show provider
  /budget [n]        show or set context budget (tokens)
  /steps [n]         show or set max agent tool steps (0=unlimited)
  /search <query>    search the web (uses DuckDuckGo, no API key needed)
  /save <name>       save session to disk
  /load <name>       load session from disk (replaces current)
  /delete <name>     delete saved session
  /list              list saved sessions
  /session           show current session info
editing keys:
  ↑ ↓                history
  ← →                cursor
  Home / End         line start/end
  Backspace / Delete character
  Ctrl+U             kill line
  Ctrl+W             kill word
  Tab                command completion
  Ctrl-D             exit
  Esc                help dialog
`
