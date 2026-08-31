package clichat

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
)

func replLineMode(sess *chat.Session, res *config.Resolved, toolsOn bool, jsonMode bool) error {
	if jsonMode {
		activeJSONSlashSink = &JSONSlashSink{w: os.Stdout}
		defer func() { activeJSONSlashSink = nil }()
	}
	defer startLineModeHub(sess, jsonMode)()
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
		if err := sendLineMode(sess, line, sigCh, jsonMode); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		}
	}
	return sc.Err()
}

// sendLineMode runs one line-mode turn. In plain mode stdout carries the raw
// streamed answer text; in --json mode (jsonMode) stdout instead carries
// NDJSON events - see ndjsonEvent for the schema - so a caller piping this
// process (e.g. a GUI wrapper) can parse chunk/turn-end/error boundaries
// without guessing from a bare trailing newline.
func sendLineMode(sess *chat.Session, line string, sigCh <-chan os.Signal, jsonMode bool) error {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go cancelOnInterrupt(ctx, cancel, done, sigCh)
	usage := sess.ContextUsage()
	fmt.Fprintf(os.Stderr, "  (~%d tokens, %d%% context used)\n", usage.UsedTokens, usage.Percent)
	var w io.Writer = os.Stdout
	var jw *ndjsonChunkWriter
	var onEvent func(agent.Event)
	if jsonMode {
		jw = newNDJSONChunkWriter(os.Stdout)
		w = jw
		// Thinking/tool_start/tool_end are written directly to os.Stdout
		// (not through jw): they are whole, self-contained NDJSON lines, not
		// raw content-delta bytes needing jw's split-UTF-8-rune buffering, so
		// routing them through jw would gain nothing and risks getting stuck
		// behind pending buffered bytes.
		onEvent = jsonTurnEventCallback(os.Stdout)
	}
	_, err := sess.SendUserWithEvent(ctx, line, w, onEvent)
	for _, note := range sess.TakeAdmissionNotes() {
		fmt.Fprintf(os.Stderr, "\n%s\n", note)
	}
	// Line mode has no periodic-save timer and no /save-on-exit UI the way
	// the TUI does, and its host process (e.g. the desktop app's sidecar) is
	// routinely killed rather than exited cleanly - so every turn must save
	// itself. SaveAfterTurn no-ops if the turn added no content.
	sess.SaveAfterTurn()
	// Read the interrupt BEFORE cancelling. This used to ask ctx.Err() after
	// its own cancel(), so every turn reported "(cancelled)" and returned nil -
	// the turn's real error was discarded on the one surface that has nowhere
	// else to show it, and a durable publication failure looked like the user
	// pressing Ctrl+C.
	interrupted := ctx.Err() != nil
	close(done)
	cancel()
	if !jsonMode {
		fmt.Fprintln(os.Stdout)
	}
	if interrupted && CancellationCanReplaceTurnError(err) {
		fmt.Fprintln(os.Stderr, "(cancelled)")
		if jsonMode {
			// The turn was interrupted mid-stream: whatever ndjsonChunkWriter
			// is still holding back as a possibly-incomplete trailing rune was
			// never a confirmed, complete chunk, so it must not be flushed -
			// that would surface a phantom chunk for content the turn never
			// finished producing. Reported as its own "cancelled" type
			// (distinct from "error") so a consumer can tell "the user
			// stopped this" apart from "this failed" without string-matching
			// a message field.
			jw.Discard()
			writeNDJSONEvent(os.Stdout, ndjsonEvent{Type: "cancelled"})
		}
		return nil
	}
	if jsonMode {
		if err != nil {
			jw.Discard()
			writeNDJSONEvent(os.Stdout, ndjsonEvent{Type: "error", Message: jsonTurnErrorMessage(err)})
			return err
		}
		jw.Flush()
		// Turn-final context accounting, emitted before "done" so a
		// consumer that finalizes turn state on "done" has already received
		// it. Only on the success path: a cancelled or errored turn's
		// growth is not final, and a consumer keeps its previous reading.
		writeContextUsageLine(os.Stdout, sess)
		writeNDJSONEvent(os.Stdout, ndjsonEvent{Type: "done", SessionID: sess.SessionID})
	}
	return err
}

// writeContextUsageLine frames the session's context accounting - the same
// numbers the TUI status dialog renders - as a "context_usage" NDJSON line.
// Extracted from sendLineMode to keep it under the structure-check
// function-size limit. A fresh ContextUsage call, not a cached pre-turn
// value, so it reflects the turn that just completed.
func writeContextUsageLine(w io.Writer, sess *chat.Session) {
	usage := sess.ContextUsage()
	writeNDJSONEvent(w, ndjsonEvent{
		Type: "context_usage",
		ContextUsage: &ndjsonContextUsage{
			UsedTokens:          usage.UsedTokens,
			BudgetTokens:        usage.BudgetTokens,
			ContextWindowTokens: usage.ContextWindowTokens,
			OutputReserveTokens: usage.OutputReserveTokens,
			Percent:             usage.Percent,
		},
	})
}

func cancelOnInterrupt(ctx context.Context, cancel context.CancelFunc, done <-chan struct{}, sigCh <-chan os.Signal) {
	select {
	case <-sigCh:
		cancel()
	case <-done:
	case <-ctx.Done():
	}
}
