package agent

import (
	"context"
	"fmt"
	"hash/fnv"

	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	sdkplan "github.com/MiviaLabs/mivia-ai-sdk/context/plan"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
)

// sdkSummarizerAdapter implements sdkagentloop.Summarizer over the
// host's governed contextmgr.Summarizer, so an SDK mid-run compaction
// gets the same redaction, policy binding, and evidence tracking a
// host-triggered compaction gets. Constructed once per turn in
// adoptSDKCompaction; never nil once constructed, so it is never the
// typed-nil case agentloop.Options.Validate's *plan.Summarizer
// assertion guards against (this is a different concrete type
// entirely).
type sdkSummarizerAdapter struct {
	l    *Loop
	opts Options
}

// sdkCompactionOutcome is one Summarize call's result, held on Loop
// (sdkPendingCompaction) until the next Options.ObserveRequest call
// confirms it. See confirmSDKCompaction for why grounding is deferred
// past Summarize itself.
type sdkCompactionOutcome struct {
	message        provider.Message // zero value on a skip with nothing injected
	summarized     bool
	reason         string
	key            string // sdkCompactionIdentity(droppedOnly); "" is never a valid key
	elidedMessages int
	elidedBytes    int
}

// noSummarizerReason is the fallback text prepareSDKOnce's PM-driven
// path already reports when SummaryConfig.Summarizer is nil, so an
// operator sees one reason string for "no summarizer" whichever path
// produced it.
const noSummarizerReason = "no summarizer is configured for this session"

// Summarize maps the SDK's Summarizer interface onto the host's
// governed contextmgr.Summarizer. msgs is the SDK's held-aside prior
// summary (if any, first element, Name == sdkplan.SummaryMessageName)
// followed by the dropped turns (agentloop's summarizeDropped
// contract). Success maps the host's 7 overlapping Summary fields
// onto sdkplan.Summary field by field. Any non-retryable failure
// (including "no summarizer wired" and a host-state read failure)
// wraps sdkplan.ErrSummarySkipped with the classified reason; a
// retryable failure returns unwrapped, so the SDK fails the whole
// compaction closed instead of silently degrading - the adapter has
// no retry loop of its own, unlike the PM-driven summarizeTurn's
// bounded inline retries.
func (a *sdkSummarizerAdapter) Summarize(ctx context.Context, msgs []sdkshape.Message) (sdkplan.Summary, error) {
	_, dropped := splitSDKPriorSummary(msgs)
	cliDropped := sdkMessagesToCLI(dropped)

	if a.opts.SummaryConfig.Summarizer == nil {
		return sdkplan.Summary{}, a.skip(cliDropped, noSummarizerReason)
	}

	snapshot, err := a.l.TurnState.Snapshot()
	if err != nil {
		return sdkplan.Summary{}, a.skip(cliDropped, contextmgr.SummaryReasonHostState)
	}

	summarizer := a.opts.SummaryConfig.Summarizer
	request, err := contextmgr.BuildSummaryRequest(contextmgr.SummaryBuildInput{
		Version:           contextmgr.SummarySchemaVersion,
		Objective:         SummaryFieldText(latestUserObjective(a.l.Messages)),
		State:             snapshot.State,
		Decisions:         snapshot.Decisions,
		Evidence:          snapshot.Evidence,
		ChangedSurfaces:   snapshot.ChangedSurfaces,
		OpenWork:          snapshot.OpenWork,
		Risks:             snapshot.Risks,
		SourceExcerpts:    contextmgr.SourceExcerpts(cliDropped, nil),
		SourceRange:       a.l.LastPreparation.Token.Range,
		PolicyDigest:      summarizer.Policy.PolicyDigest,
		Provider:          summarizer.Binding.Provider,
		Model:             summarizer.Binding.Model,
		EndpointAllowlist: summarizer.Policy.EndpointAllowlist,
		RedactionPolicy:   a.opts.SummaryConfig.Redaction,
		Budget:            SummaryRequestBudget(a.opts.MaxContextTokens),
		OutputLimit:       SummaryOutputLimitTokens,
	})
	if err != nil {
		return sdkplan.Summary{}, a.skip(cliDropped, contextmgr.SummaryReasonRequestInvalid)
	}

	summary, err := summarizer.Summarize(ctx, request)
	if err != nil {
		reason := contextmgr.ClassifySummaryFailure(err)
		if !contextmgr.RetryableSummaryFailure(err) {
			return sdkplan.Summary{}, a.skip(cliDropped, reason)
		}
		a.l.summaryFailureReason = reason
		return sdkplan.Summary{}, err
	}

	value := summary.Value()
	rendered := RenderSummaryMessage(summary, request.Input.Evidence)
	a.l.sdkPendingCompaction = &sdkCompactionOutcome{
		message:        rendered,
		summarized:     true,
		key:            sdkCompactionIdentity(cliDropped),
		elidedMessages: len(cliDropped),
		elidedBytes:    sdkElidedBytes(cliDropped),
	}
	a.l.summaryFailureReason = ""
	return sdkplan.Summary{
		Objective:       value.Objective,
		State:           value.State,
		Decisions:       value.Decisions,
		Evidence:        value.Evidence,
		ChangedSurfaces: value.ChangedSurfaces,
		OpenWork:        value.OpenWork,
		Risks:           value.Risks,
	}, nil
}

// skip records the pending outcome for a real-but-unsummarized
// compaction (the dropped messages were still elided from the
// model's context even though no summary exists for them) and
// returns the wrapped skip sentinel. agentloop's summarizeDropped
// matches it through errors.Is regardless of the wrap.
func (a *sdkSummarizerAdapter) skip(dropped []provider.Message, reason string) error {
	a.l.summaryFailureReason = reason
	a.l.sdkPendingCompaction = &sdkCompactionOutcome{
		summarized:     false,
		reason:         reason,
		key:            sdkCompactionIdentity(dropped),
		elidedMessages: len(dropped),
		elidedBytes:    sdkElidedBytes(dropped),
	}
	return fmt.Errorf("%w: %s", sdkplan.ErrSummarySkipped, reason)
}

// splitSDKPriorSummary removes the held-aside prior summary message,
// when present, from the front of msgs and returns it separately from
// the dropped turns. The prior is already a summary, not raw
// conversation to excerpt.
func splitSDKPriorSummary(msgs []sdkshape.Message) (prior *sdkshape.Message, dropped []sdkshape.Message) {
	if len(msgs) > 0 && msgs[0].Name == sdkplan.SummaryMessageName {
		p := msgs[0]
		return &p, msgs[1:]
	}
	return nil, msgs
}

// sdkCompactionIdentity derives a deterministic identity for one real
// compaction from its dropped set's content, so two distinct
// compactions in the same turn ground and emit independently while a
// re-observed (already-confirmed) outcome does not re-emit. Mirrors
// compactionIdentity's role for the PM-driven path, which keys off a
// contextmgr.CommitToken that does not exist at this call site.
func sdkCompactionIdentity(dropped []provider.Message) string {
	if len(dropped) == 0 {
		return ""
	}
	h := fnv.New64a()
	for _, m := range dropped {
		_, _ = h.Write([]byte(m.Role))
		_, _ = h.Write([]byte(m.Content))
	}
	return fmt.Sprintf("sdk:%x", h.Sum64())
}

func sdkElidedBytes(msgs []provider.Message) int {
	n := 0
	for _, m := range msgs {
		n += len(m.Content)
	}
	return n
}
