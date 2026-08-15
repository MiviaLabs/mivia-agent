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
//
// The second result carries the rendered summary message to the commit path.
// The compaction has already dropped the summarized messages for good, so
// unless that message joins the turn's committed active context the account of
// them dies with this request and every later turn sees a truncated history
// explaining nothing.
func injectPlainSummary(ctx context.Context, snapshot plainTurnSnapshot, preparation contextmgr.Preparation, prepared []provider.Message) ([]provider.Message, injectedSummary) {
	summarizer := snapshot.context.summarizer
	if summarizer == nil || !preparation.Compacted {
		return prepared, injectedSummary{}
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
		return prepared, injectedSummary{}
	}
	summary, err := summarizer.Summarize(ctx, request)
	if err != nil {
		return prepared, injectedSummary{}
	}
	// Render the sealed summary together with the host-side omitted-evidence
	// diff (request.Input.Evidence), mirroring the agent-loop path.
	injected := agent.RenderSummaryMessage(summary, request.Input.Evidence)
	if agent.SummaryOverBudget(preparation.AfterTokens, injected, snapshot.budget) {
		return prepared, injectedSummary{}
	}
	return agent.InjectSummaryMessage(prepared, injected), injectedSummary{message: injected, present: true}
}

// injectedSummary carries one turn's rendered context summary from the request
// path to the commit path. The zero value means the turn injected none.
type injectedSummary struct {
	message provider.Message
	present bool
}

// appendTo returns messages with the summary appended when there is one. It is
// applied to the committed active context and the live history, never to the
// `ordered` slice that feeds source projection: the message is host-generated
// and has no source event of its own.
func (s injectedSummary) appendTo(messages []provider.Message) []provider.Message {
	if !s.present {
		return messages
	}
	return append(cloneContextMessages(messages), s.message)
}
