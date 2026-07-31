// Package cli implements mivia command handlers.
package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
)

func oneShot(sess *chat.Session, prompt string, toolsOn bool, res *config.Resolved) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	mode := "chat"
	if toolsOn {
		mode = "agent"
	}
	fmt.Fprintf(os.Stderr, "%smivia%s %s  provider=%s model=%s\n", ansiCyan, ansiReset, mode, res.ProviderName, sess.CurrentModel())
	start := time.Now()

	// Collect assistant text then render markdown to stdout for nicer one-shots.
	var raw strings.Builder
	mw := NewMarkdownWriter(&raw)
	finalW := io.Writer(mw)
	if toolsOn {
		r := NewChatRenderer(&stderrTerm{}, sess.CurrentModel())
		ui, h := newClassicAgentHandler(r)
		sess.OnAgentEvent = h
		finalW = wrapClassicBufferedFinalWriter(ui, mw)
	}

	_, err := sess.SendUser(ctx, prompt, finalW)
	if cw, ok := finalW.(*classicStreamWriter); ok {
		cw.commit()
	}
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
	fmt.Fprintf(os.Stderr, "%s  ─ done · %s ─%s\n", ansiDim, formatDuration(time.Since(start)), ansiDimEnd)
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
	line, stop, err := preprocessChatLine(line, sess, res, toolsOn, term, renderer)
	if stop {
		return err
	}
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

	renderer.PrintUser(line)
	renderer.PrintAssistantHeader()
	mw := NewMarkdownWriter(term)
	finalW := io.Writer(mw)
	if toolsOn {
		ui, h := newClassicAgentHandler(renderer)
		sess.OnAgentEvent = h
		finalW = wrapClassicFinalWriter(ui, mw)
	}
	_, err = sess.SendUser(ctx, line, finalW)
	if cw, ok := finalW.(*classicStreamWriter); ok {
		cw.commit()
	}
	_ = mw.Flush()
	term.WriteString("\n")
	input.RenderInPlace(term)
	close(done)
	if err != nil {
		if ctx.Err() != nil {
			renderer.PrintInfo("(cancelled — still in session; /exit to quit)")
			return nil
		}
		renderer.PrintError(err.Error())
	}
	return nil
}

// preprocessChatLine handles /search rewrite, slash commands, and exit.
// stop=true means the line was fully handled (do not send to model).
func preprocessChatLine(line string, sess *chat.Session, res *config.Resolved, toolsOn bool, term *Terminal, renderer *ChatRenderer) (string, bool, error) {
	if strings.HasPrefix(line, "/search") {
		query := strings.TrimSpace(strings.TrimPrefix(line, "/search"))
		if query == "" {
			renderer.PrintInfo("usage: /search <query> — searches the web and returns AI-synthesized results")
			return line, true, nil
		}
		return "search the web for: " + query, false, nil
	}
	if strings.HasPrefix(line, "/") {
		if handled, exit, herr := handleSlash(line, sess, res, toolsOn, term); handled {
			if herr != nil {
				renderer.PrintError(herr.Error())
			}
			_ = exit
			return line, true, nil
		}
	}
	if line == "exit" || line == "quit" {
		return line, true, nil
	}
	return line, false, nil
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

// shortenWorkspacePath returns the current directory with the home prefix
// collapsed to ~, or "" when unavailable.
func shortenWorkspacePath() string {
	wd, err := os.Getwd()
	if err != nil || wd == "" {
		return ""
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(wd, home) {
		return "~" + strings.TrimPrefix(wd, home)
	}
	return wd
}

// replLineMode is the fallback when stdin is not a terminal.

const slashHelp = `commands:
  /help              show this help
  /exit /quit /q     leave
  /clear             clear conversation history
  /new               start a new session (current one saved)
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
