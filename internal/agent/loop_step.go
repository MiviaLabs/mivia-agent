package agent

import (
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/redact"
)

// initialToolSpecs is the turn's step-1 advertised array, before the SDK's
// per-iteration Options.Surface hook can rotate it (bridgeSDKBridgeSurface
// skips the first iteration, mirroring the legacy skip-step-1 rule). A
// host-pinned snapshot (plan tools-advertising/01) takes over the whole
// turn; without one, fall back to the live registry (today's behavior -
// subagent and workflow-engine loops).
func (l *Loop) initialToolSpecs(opts Options) []provider.ToolSpec {
	if opts.AdvertisedToolSpecs != nil {
		return opts.AdvertisedToolSpecs
	}
	return l.Tools.OpenAITools()
}

// emitReasoning surfaces model chain of thought when the provider exposes
// it. The event sink gets a redacted copy: reasoning is operator-facing, so it
// passes through the workspace's redaction policy before reaching OnEvent
// consumers (redact.Text is an identity when no policy is installed).
// Persistence into host history is separate and stays verbatim (the SDK
// completer wrapper copies resp.ReasoningContent onto the assistant
// Message), because the provider that produced the reasoning needs the raw
// bytes back on replay.
func emitReasoning(opts Options, resp *provider.Response) {
	if resp == nil || resp.ReasoningContent == "" {
		return
	}
	emit(opts, Event{Kind: EventThinking, Content: redact.Text(resp.ReasoningContent)})
}
