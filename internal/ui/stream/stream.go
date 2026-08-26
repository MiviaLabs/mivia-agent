// Package stream is the non-TTY plain renderer: readable text with no
// ANSI escapes, no theme dependency, safe to pipe. It proves the
// uievent.Event stream renders coherently before any pixel exists.
package stream

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// Render writes a plain-text transcript of events to w, one line (or
// small block) per event. Kinds are handled exhaustively via a type
// switch on Body; an unrecognised Body is a programmer error, not a
// silent skip.
func Render(w io.Writer, events []uievent.Event) error {
	for _, ev := range events {
		if err := renderOne(w, ev); err != nil {
			return fmt.Errorf("stream: render %s: %w", ev.Kind, err)
		}
	}
	return nil
}

func renderOne(w io.Writer, ev uievent.Event) error {
	switch b := ev.Body.(type) {
	case uievent.TurnStartBody:
		return printBlock(w, "> ", b.Input)
	case uievent.TextDeltaBody:
		return nil // accumulated text is printed once, on TextEndBody
	case uievent.TextEndBody:
		if b.Text == "" {
			return nil
		}
		if _, err := fmt.Fprintln(w); err != nil {
			return err
		}
		return printBlock(w, "", b.Text)
	case uievent.ReasoningDeltaBody:
		if b.WordCount == 0 {
			return nil // mid-span delta; only the final chunk carries a count
		}
		_, err := fmt.Fprintf(w, "> reasoning %d words hidden\n", b.WordCount)
		return err
	case uievent.ToolPendingBody:
		_, err := fmt.Fprintf(w, "? approve %s %s\n", b.Name, formatArgs(b.Args))
		return err
	case uievent.ToolStartBody:
		_, err := fmt.Fprintf(w, "v %-12s %s\n", b.Name, formatArgs(b.Args))
		return err
	case uievent.ToolOutputBody:
		return renderToolOutput(w, b)
	case uievent.ToolEndBody:
		return renderToolEnd(w, b)
	case uievent.PlanBody:
		return renderPlan(w, b)
	case uievent.NoticeBody:
		_, err := fmt.Fprintf(w, "  notice  %s\n", b.Text)
		return err
	case uievent.HookBody:
		return renderHook(w, b)
	case uievent.ErrorBody:
		return renderError(w, b)
	case uievent.UsageBody:
		_, err := fmt.Fprintf(w, "  usage   %d in  %d out  %d cached  $%.3f  %.1fs\n",
			b.InputTokens, b.OutputTokens, b.CachedTokens, b.CostUSD, b.ElapsedSeconds)
		return err
	case uievent.TurnEndBody:
		_, err := fmt.Fprintf(w, "\n> (turn %s)\n", b.Reason)
		return err
	default:
		return fmt.Errorf("unhandled body type %T", b)
	}
}

func printBlock(w io.Writer, prefix, text string) error {
	for _, line := range strings.Split(text, "\n") {
		if _, err := fmt.Fprintf(w, "%s%s\n", prefix, line); err != nil {
			return err
		}
		prefix = strings.Repeat(" ", len(prefix))
	}
	return nil
}

func renderError(w io.Writer, b uievent.ErrorBody) error {
	// Defensive guard: an empty-text error produces "  error" with no body,
	// which reads as a bare "error" line and tells the user nothing about
	// what failed. Mirror the TextEndBody early-return at line 35 so a
	// downstream producer that emits a malformed empty-text error is
	// suppressed at the renderer rather than mis-formatted. The typed
	// KindError event itself is preserved on the channel; only the
	// rendered transcript drops it.
	if strings.TrimSpace(b.Text) == "" && !b.Fatal {
		return nil
	}
	lines := strings.Split(b.Text, "\n")
	status := ""
	if b.Fatal {
		status = "fatal"
	}
	if _, err := fmt.Fprintf(w, "  error   %-56s %s\n", lines[0], status); err != nil {
		return err
	}
	for _, line := range lines[1:] {
		if _, err := fmt.Fprintf(w, "          %s\n", line); err != nil {
			return err
		}
	}
	return nil
}

// renderHook prints one lifecycle-hook execution: which program fired, for
// which tool and event, and what it said - the plain-text mirror of
// transcript's hookBlockValue. A denied run is marked so a piped log still
// shows why a call was blocked.
func renderHook(w io.Writer, b uievent.HookBody) error {
	program := b.Program
	if program == "" {
		program = "(unnamed hook)"
	}
	tool := b.Tool
	if tool == "" {
		tool = "(no tool)"
	}
	status := ""
	if b.Denied {
		status = "blocked"
	}
	if _, err := fmt.Fprintf(w, "  hook    %s (%s) -> %s %s\n", program, b.Event, tool, status); err != nil {
		return err
	}
	if b.Input != "" {
		if _, err := fmt.Fprintf(w, "          in:  %s\n", b.Input); err != nil {
			return err
		}
	}
	if b.Output != "" {
		if _, err := fmt.Fprintf(w, "          out: %s\n", b.Output); err != nil {
			return err
		}
	}
	return nil
}

func renderToolOutput(w io.Writer, b uievent.ToolOutputBody) error {
	if b.Progress != nil {
		p := b.Progress
		if _, err := fmt.Fprintf(w, "    [%d of %d] %s %.0fs\n", p.Step, p.TotalSteps, p.Status, p.ElapsedSeconds); err != nil {
			return err
		}
		for _, line := range p.Log {
			if _, err := fmt.Fprintf(w, "    %s\n", line); err != nil {
				return err
			}
		}
		return nil
	}
	if b.Chunk == "" {
		return nil
	}
	first := strings.SplitN(b.Chunk, "\n", 2)[0]
	_, err := fmt.Fprintf(w, "    %s\n", first)
	return err
}

func renderToolEnd(w io.Writer, b uievent.ToolEndBody) error {
	status := "ok"
	if !b.OK {
		status = "failed"
	}
	summary := b.Result
	if b.Err != "" {
		summary = b.Err
	}
	if _, err := fmt.Fprintf(w, "  %-11s %-40s %6dms  %s\n", b.Name, summary, b.DurationMS, status); err != nil {
		return err
	}
	if b.Diff == nil {
		return nil
	}
	if _, err := fmt.Fprintf(w, "    %s  +%d -%d\n", b.Diff.Path, b.Diff.Added, b.Diff.Removed); err != nil {
		return err
	}
	for _, hunk := range b.Diff.Hunks {
		if _, err := fmt.Fprintf(w, "    %s\n", hunk.Header); err != nil {
			return err
		}
		for _, line := range hunk.Lines {
			marker := " "
			switch line.Kind {
			case uievent.DiffLineAdd:
				marker = "+"
			case uievent.DiffLineDel:
				marker = "-"
			}
			if _, err := fmt.Fprintf(w, "    %s%s\n", marker, line.Text); err != nil {
				return err
			}
		}
	}
	return nil
}

func renderPlan(w io.Writer, b uievent.PlanBody) error {
	if _, err := fmt.Fprintf(w, "  plan    %d of %d\n", b.Done, b.Total); err != nil {
		return err
	}
	for _, item := range b.Items {
		mark := "[ ]"
		if item.Done {
			mark = "[x]"
		}
		if _, err := fmt.Fprintf(w, "    %s %s\n", mark, item.Text); err != nil {
			return err
		}
	}
	return nil
}

func formatArgs(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, args[k]))
	}
	return strings.Join(parts, " ")
}
