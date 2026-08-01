package agent

import (
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

// maxHookEventOutput bounds one hook row's text.
//
// The model's copy is bounded separately and generously, because the model has
// to reason about it. This copy is a transcript row a person reads at a glance,
// and 8 KiB of formatter output pushed through the TUI would bury the tool it
// is describing.
const maxHookEventOutput = 512

// emitHookRuns reports each lifecycle hook that fired for one tool call.
//
// Every run produces a row, including a silent one. That is the point rather
// than an oversight: a hook is a program the runtime executes on the operator's
// machine on every matching call, and the honest answer to "did my formatter
// run?" cannot be "only if it printed something". A mis-typed matcher that
// silently selects nothing looks identical to a working hook until these rows
// exist.
func emitHookRuns(opts Options, toolCallID string, runs []runtime.HookRun) {
	for _, run := range runs {
		emit(opts, Event{
			Kind:       EventHook,
			ToolCallID: toolCallID,
			Name:       run.Event,
			Detail:     hookRunDetail(run),
			Output:     hookRunOutput(run),
		})
	}
}

// hookRunDetail is the row's one-line summary: which script, and what it did.
func hookRunDetail(run runtime.HookRun) string {
	program := run.Program
	if program == "" {
		program = "(unnamed hook)"
	}
	switch {
	case run.Denied:
		return fmt.Sprintf("%s blocked the call", program)
	case run.Warning != "":
		return fmt.Sprintf("%s warned", program)
	case run.Output != "":
		return fmt.Sprintf("%s ran", program)
	default:
		return fmt.Sprintf("%s ran, no output", program)
	}
}

// hookRunOutput is what the hook said. A diagnostic is appended rather than
// substituted: a hook can both produce advice and misbehave, and dropping
// either half would misreport the run.
func hookRunOutput(run runtime.HookRun) string {
	parts := make([]string, 0, 2)
	if text := strings.TrimSpace(run.Output); text != "" {
		parts = append(parts, text)
	}
	if warning := strings.TrimSpace(run.Warning); warning != "" {
		parts = append(parts, warning)
	}
	return truncatePreview(strings.Join(parts, "\n"), maxHookEventOutput)
}
