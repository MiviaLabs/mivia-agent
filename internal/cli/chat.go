// Package cli implements mivia command handlers.
package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
)

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
	fmt.Fprintf(os.Stderr, "%smivia%s %s  provider=%s model=%s\n", ansiCyan, ansiReset, mode, res.ProviderName, sess.Model)
	start := time.Now()

	// Tool events with elapsed on stderr.
	if toolsOn {
		r := NewChatRenderer(&stderrTerm{}, sess.Model)
		sess.OnAgentEvent = makeAgentUIWithRenderer(r)
	}

	// Collect assistant text then render markdown to stdout for nicer one-shots.
	var raw strings.Builder
	mw := NewMarkdownWriter(&raw)
	_, err := sess.SendUser(ctx, prompt, mw)
	_ = mw.Flush()
	if err != nil {
		if ctx.Err() != nil {
			fmt.Fprintln(os.Stderr, "\n(cancelled)")
			return nil
		}
		return err
	}
	// raw already has ANSI from MarkdownWriter
	fmt.Fprint(os.Stdout, raw.String())
	if !strings.HasSuffix(raw.String(), "\n") {
		fmt.Fprintln(os.Stdout)
	}
	fmt.Fprintf(os.Stderr, "%s  â”€ done Â· %s â”€%s\n", ansiDim, formatDuration(time.Since(start)), ansiDimEnd)
	return nil
}

// stderrTerm adapts stderr for ChatRenderer in one-shot mode.
type stderrTerm struct{}

func (stderrTerm) Write(p []byte) (int, error) { return os.Stderr.Write(p) }
func (stderrTerm) WriteString(s string)        { fmt.Fprint(os.Stderr, s) }
func (stderrTerm) Size() (int, int)            { return 80, 24 }

// processLineChat handles a committed input line with chat-style formatting.
// All output goes to the terminal (stderr) in REPL mode, not stdout.
func processLineChat(line string, sess *chat.Session, res *config.Resolved, toolsOn bool, term *Terminal, renderer *ChatRenderer, input *InputBuffer, modelShort string) error {
	// Transform /search <query> to a natural language request that routes
	// through the AI model â€” the model calls the search tool, gets results,
	// and returns a synthesized answer with proper formatting.
	if strings.HasPrefix(line, "/search") {
		query := strings.TrimSpace(strings.TrimPrefix(line, "/search"))
		if query == "" {
			renderer.PrintInfo("usage: /search <query> â€” searches the web and returns AI-synthesized results")
			return nil
		}
		line = "search the web for: " + query
		// Fall through to the AI path below â€” don't handle as a slash command.
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
	// the input buffer â€” no need to clear anything since the prompt was drawn
	// at the bottom and we write to stderr which scrolls naturally.
	renderer.PrintUser(line)

	// Print assistant header before model response starts.
	renderer.PrintAssistantHeader()

	// Send to model â€” wrap term with MarkdownWriter so streaming markdown
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
			renderer.PrintInfo("(cancelled â€” still in session; /exit to quit)")
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
  /search <query>    search the web (multiple free engines, no API key)
  /save <name>       save session to disk
  /load <name>       load session from disk (replaces current)
  /delete <name>     delete saved session
  /list              list saved sessions
  /session           show current session info
editing keys:
  â†‘ â†“                history
  â† â†’                cursor
  Home / End         line start/end
  Backspace / Delete character
  Ctrl+U             kill line
  Ctrl+W             kill word
  Tab                command completion
  Ctrl-D             exit
  Esc                help dialog
`
