package chat

import (
	"context"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// buildCompactSummaryRequest derives the manual-compact summary request from
// the pre-compaction history, the retained preparation, and the captured
// summarizer policy. The objective is the latest user message; the evidence
// is the host-side content-free diff of what the compact dropped.
// OmittedEvidence owns the distinctness rule (many omitted messages share one
// size bucket, and the envelope validator refuses duplicate evidence items),
// so this path no longer dedupes a second time.
func buildCompactSummaryRequest(summarizer *contextmgr.Summarizer, redaction contextstate.RedactionPolicy, budget int, pre, retained []provider.Message, sourceRange contextstate.SourceRange, focus string) (contextmgr.SummaryRequest, error) {
	return contextmgr.BuildSummaryRequest(contextmgr.SummaryBuildInput{
		Version:           contextmgr.SummarySchemaVersion,
		Objective:         agent.SummaryFieldText(latestUserMessage(pre)),
		Evidence:          contextmgr.OmittedEvidence(pre, retained),
		SourceExcerpts:    contextmgr.SourceExcerpts(pre, retained),
		SourceRange:       sourceRange,
		PolicyDigest:      summarizer.Policy.PolicyDigest,
		Provider:          summarizer.Binding.Provider,
		Model:             summarizer.Binding.Model,
		EndpointAllowlist: summarizer.Policy.EndpointAllowlist,
		RedactionPolicy:   redaction,
		Budget:            agent.SummaryRequestBudget(budget),
		OutputLimit:       agent.SummaryOutputLimitTokens,
		Focus:             focus,
	})
}

// applyCompactSummary runs the manual-compact summary and renders its two
// outcomes: bounded durable metadata for the checkpoint candidate, and the
// rendered context-summary message for the live session history. Any failure
// - builder error, summarizer error, policy refusal, metadata bound - returns
// ok=false so the structural compact proceeds unchanged. A summary must never
// fail a manual compact.
func applyCompactSummary(ctx context.Context, summarizer *contextmgr.Summarizer, redaction contextstate.RedactionPolicy, budget int, pre, retained []provider.Message, sourceRange contextstate.SourceRange, focus string) ([]byte, provider.Message, bool) {
	if summarizer == nil {
		return nil, provider.Message{}, false
	}
	request, err := buildCompactSummaryRequest(summarizer, redaction, budget, pre, retained, sourceRange, focus)
	if err != nil {
		return nil, provider.Message{}, false
	}
	summary, err := summarizer.Summarize(ctx, request)
	if err != nil {
		return nil, provider.Message{}, false
	}
	metadata, err := summary.Metadata(summarizer.Policy.RedactionConfigured)
	if err != nil {
		return nil, provider.Message{}, false
	}
	return metadata, agent.RenderSummaryMessage(summary, request.Input.Evidence), true
}

// summarizeManualCompact runs the wired summarizer for one manual compact:
// it stamps the bounded metadata on the preparation's checkpoint candidate
// and returns the rendered message for the live history. The returned message
// is ANONYMOUS: the wire Name marks ephemeral injections, and every restore
// path runs provider.ValidateToolPairing, which refuses NAMED user messages -
// a named summary made the session unresumable after one more turn. A nil
// summarizer or any summary failure changes nothing and reports have=false.
func summarizeManualCompact(ctx context.Context, cfg contextTurnConfig, input contextmgr.PrepareInput, pre []provider.Message, preparation *contextmgr.Preparation, focus string) (provider.Message, bool) {
	if cfg.summarizer == nil {
		return provider.Message{}, false
	}
	metadata, injected, ok := applyCompactSummary(ctx, cfg.summarizer, cfg.redaction, input.Budget, pre, preparation.Messages, preparation.Token.Range, focus)
	if !ok {
		return provider.Message{}, false
	}
	preparation.Candidate.SummaryMetadata = metadata
	injected.Name = ""
	return injected, true
}
