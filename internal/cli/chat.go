// Package cli implements mivia command handlers.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

func oneShot(sess *chat.Session, prompt string, toolsOn bool, res *config.Resolved) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return oneShotContext(ctx, sess, prompt, toolsOn, res)
}

func oneShotContext(ctx context.Context, sess *chat.Session, prompt string, toolsOn bool, res *config.Resolved) error {
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
	// A deferred admission has no other surface in one-shot mode: the process
	// exits after this turn, so an undrained note is never seen at all. It goes
	// to stderr because stdout is the answer channel a caller pipes elsewhere.
	for _, note := range sess.TakeAdmissionNotes() {
		fmt.Fprintf(os.Stderr, "%s\n", note)
	}
	if shouldPrintOneShotOutput(ctx, err) {
		fmt.Fprint(os.Stdout, raw.String())
		if !strings.HasSuffix(raw.String(), "\n") {
			fmt.Fprintln(os.Stdout)
		}
	}
	if err != nil {
		if ctx.Err() != nil && cancellationCanReplaceTurnError(err) {
			fmt.Fprintln(os.Stderr, "\n(cancelled)")
			return nil
		}
		return err
	}
	fmt.Fprintf(os.Stderr, "%s  ─ done · %s ─%s\n", ansiDim, formatDuration(time.Since(start)), ansiDimEnd)
	return nil
}

func shouldPrintOneShotOutput(ctx context.Context, err error) bool {
	return err == nil || errors.Is(err, chat.ErrPersistence) || (ctx.Err() != nil && cancellationCanReplaceTurnError(err))
}

func cancellationCanReplaceTurnError(err error) bool {
	return err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// stderrTerm adapts stderr for ChatRenderer in one-shot mode.
type stderrTerm struct{}

func (stderrTerm) Write(p []byte) (int, error) { return os.Stderr.Write(p) }
func (stderrTerm) WriteString(s string)        { fmt.Fprint(os.Stderr, s) }
func (stderrTerm) Size() (int, int)            { return defaultTermWidth, defaultTermHeight }

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
	// The classic interactive REPL is a turn-completion surface of its own; the
	// note queue is drained here for the same reason line mode drains it, or a
	// deferred admission stays invisible until some other surface picks it up.
	for _, note := range sess.TakeAdmissionNotes() {
		renderer.PrintDim("%s", note)
	}
	term.WriteString("\n")
	input.RenderInPlace(term)
	close(done)
	if err != nil {
		if ctx.Err() != nil && cancellationCanReplaceTurnError(err) {
			renderer.PrintInfo("(cancelled - still in session; /exit to quit)")
			return nil
		}
		renderer.PrintError(err.Error())
	}
	return nil
}

// preprocessChatLine handles /search rewrite, slash commands, and exit.
// stop=true means the line was fully handled (do not send to model).
func preprocessChatLine(line string, sess *chat.Session, res *config.Resolved, toolsOn bool, term *Terminal, renderer *ChatRenderer) (string, bool, error) {
	fields := strings.Fields(line)
	if len(fields) > 0 && strings.EqualFold(fields[0], "/search") {
		query := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), fields[0]))
		if query == "" {
			renderer.PrintInfo("usage: /search <query> - searches the web and returns AI-synthesized results")
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
	commands := slashCommands(slashSurfacePlain, nil)
	known := make([]string, 0, len(commands)*2)
	for _, command := range commands {
		known = append(known, command.Name)
		known = append(known, command.Aliases...)
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
	if home, err := workspace.UserHomeDir(); err == nil && home != "" {
		if rel, err := filepath.Rel(home, wd); err == nil {
			if rel == "." {
				return "~"
			}
			if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return filepath.Join("~", rel)
			}
		}
	}
	return wd
}

// replLineMode is the fallback when stdin is not a terminal.
