package agent

import (
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/redact"
)

// applySurfaceHook applies the host's per-step surface at the top of every
// step after the first. Non-nil fields replace this step's registry,
// dispatcher, specs, and spool; the host supplies them from one consistent
// read (M3 invariant). Step 1 is skipped: nothing has run yet, so no
// mid-turn publication can be pending (a tool staged by load_tools only
// exists after step 1's tool calls), and publishing there would preempt a
// turn that dies at its first provider call before any tool it staged could
// take effect.
func (l *Loop) applySurfaceHook(opts *Options, toolSpecs *[]provider.ToolSpec, step int) {
	if opts.Surface == nil || step <= 1 {
		return
	}
	surf := opts.Surface()
	if surf.Registry != nil {
		l.Tools = surf.Registry
	}
	if surf.Dispatcher != nil {
		opts.Dispatcher = surf.Dispatcher
	}
	if surf.ToolSpecs != nil {
		*toolSpecs = surf.ToolSpecs
	}
	if surf.RemainderSpool != nil {
		opts.RemainderSpool = surf.RemainderSpool
	}
}

// emitReasoning surfaces model chain of thought when the provider exposes
// it. The event sink gets a redacted copy: reasoning is operator-facing, so it
// passes through the workspace's redaction policy before reaching OnEvent
// consumers (redact.Text is an identity when no policy is installed).
// Persistence into host history is separate and stays verbatim
// (commitFinalAnswer / processToolCalls copy resp.ReasoningContent onto the
// assistant Message), because the provider that produced the reasoning needs
// the raw bytes back on replay.
func emitReasoning(opts Options, resp *provider.Response) {
	if resp == nil || resp.ReasoningContent == "" {
		return
	}
	emit(opts, Event{Kind: EventThinking, Content: redact.Text(resp.ReasoningContent)})
}

func (l *Loop) emitStep(opts Options, step int) {
	d := fmt.Sprintf("%d/∞", step)
	if opts.MaxSteps > 0 {
		d = fmt.Sprintf("%d/%d", step, opts.MaxSteps)
	}
	emit(opts, Event{Kind: EventStep, Detail: d})
}
