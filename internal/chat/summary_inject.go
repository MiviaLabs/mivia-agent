package chat

import (
	"context"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// injectPlainSummary renders the validated summary of a compacted plain-context
// preparation into an EPHEMERAL clone of the request messages. The diff is the
// pre-compaction snapshot against the retained preparation; the budget
// re-check uses the snapshot budget. On any failure - builder error, summarizer
// error, policy refusal, or over-budget - it returns the structural messages
// unchanged and the turn proceeds without a summary. The caller commits the
// structural `prepared` slice, never the returned one.
func injectPlainSummary(ctx context.Context, snapshot plainTurnSnapshot, preparation contextmgr.Preparation, prepared []provider.Message) []provider.Message {
	summarizer := snapshot.context.summarizer
	if summarizer == nil || !preparation.Compacted {
		return prepared
	}
	request, err := contextmgr.BuildSummaryRequest(contextmgr.SummaryBuildInput{
		Version:           contextmgr.SummarySchemaVersion,
		Objective:         agent.SummaryFieldText(latestUserMessage(snapshot.messages)),
		Evidence:          contextmgr.OmittedEvidence(snapshot.messages, preparation.Messages),
		SourceExcerpts:    contextmgr.SourceExcerpts(snapshot.messages, preparation.Messages),
		SourceRange:       preparation.Token.Range,
		PolicyDigest:      summarizer.Policy.PolicyDigest,
		Provider:          summarizer.Binding.Provider,
		Model:             summarizer.Binding.Model,
		EndpointAllowlist: summarizer.Policy.EndpointAllowlist,
		RedactionPolicy:   snapshot.context.redaction,
		Budget:            agent.SummaryRequestBudget(snapshot.budget),
		OutputLimit:       agent.SummaryOutputLimitTokens,
	})
	if err != nil {
		return prepared
	}
	summary, err := summarizer.Summarize(ctx, request)
	if err != nil {
		return prepared
	}
	// Render the sealed summary together with the host-side omitted-evidence
	// diff (request.Input.Evidence), mirroring the agent-loop path.
	injected := agent.RenderSummaryMessage(summary, request.Input.Evidence)
	if agent.SummaryOverBudget(preparation.AfterTokens, injected, snapshot.budget) {
		return prepared
	}
	return agent.InjectSummaryMessage(prepared, injected)
}
