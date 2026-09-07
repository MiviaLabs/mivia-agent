package agent

import (
	"context"
	"fmt"
	"hash/fnv"

	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
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
	// beforeTokens/afterTokens price what this compaction replaced against
	// what it left behind, using the host's own EstimatePromptCost. The SDK
	// path has no PreparationManager output to carry them, and
	// confirmSDKCompaction's synthetic Preparation is the turn's FIRST
	// Compacted record - so without these, context.go latches 0/0 for the
	// whole turn and every compaction event, operator banner, and durable
	// usage record reads "0 -> 0 tokens".
	beforeTokens int
	afterTokens  int
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
// compaction closed instead of silently degrading.
//
// The call is memoized for the turn on its INPUT key (see
// sdkSummarizeInputKey): a repeated call over the same dropped set
// replays the first result byte for byte and issues no new provider
// request. A retryable failure is retried exactly once inside the
// same call; a second failure returns and stores no memo, so the
// SDK's own recovery may still try again.
func (a *sdkSummarizerAdapter) Summarize(ctx context.Context, msgs []sdkshape.Message) (sdkplan.Summary, error) {
	prior, dropped := splitSDKPriorSummary(msgs)
	cliDropped := sdkMessagesToCLI(dropped)
	var cliPrior []provider.Message
	if prior != nil {
		cliPrior = sdkMessagesToCLI([]sdkshape.Message{*prior})
	}
	key := sdkSummarizeInputKey(prior, cliDropped)
	if memo := a.memoized(key); memo != nil {
		return a.replayMemo(memo)
	}

	if a.opts.SummaryConfig.Summarizer == nil {
		return sdkplan.Summary{}, a.skip(cliDropped, noSummarizerReason, key)
	}

	snapshot, err := a.l.TurnState.Snapshot()
	if err != nil {
		return sdkplan.Summary{}, a.skip(cliDropped, contextmgr.SummaryReasonHostState, key)
	}

	summarizer := a.opts.SummaryConfig.Summarizer
	request, err := a.buildRequest(snapshot, cliDropped)
	if err != nil {
		return sdkplan.Summary{}, a.skip(cliDropped, contextmgr.SummaryReasonRequestInvalid, key)
	}

	summary, err := summarizeWithOneRetry(ctx, summarizer, request)
	if err != nil {
		reason := contextmgr.ClassifySummaryFailure(err)
		if !contextmgr.RetryableSummaryFailure(err) {
			return sdkplan.Summary{}, a.skip(cliDropped, reason, key)
		}
		a.l.summaryFailureReason = reason
		return sdkplan.Summary{}, err
	}
	return a.succeed(cliDropped, cliPrior, summary, request, key), nil
}

// buildRequest builds the governed summary request from the turn-state
// snapshot and the dropped messages. Evidence comes from the
// snapshot: it is the host's own content-free record of what the
// compaction removed. SourceExcerpts come from the dropped messages
// themselves. The two carry different data from different sources.
// See TestSDKSummarizerAdapterEvidenceProvenance, which pins both.
func (a *sdkSummarizerAdapter) buildRequest(snapshot contextmgr.TurnStateSnapshot, cliDropped []provider.Message) (contextmgr.SummaryRequest, error) {
	summarizer := a.opts.SummaryConfig.Summarizer
	return contextmgr.BuildSummaryRequest(contextmgr.SummaryBuildInput{
		Version:           contextmgr.SummarySchemaVersion,
		Objective:         SummaryFieldText(latestUserObjective(a.l.Messages)),
		State:             snapshot.State,
		Decisions:         snapshot.Decisions,
		Evidence:          snapshot.Evidence,
		ChangedSurfaces:   snapshot.ChangedSurfaces,
		OpenWork:          snapshot.OpenWork,
		Risks:             snapshot.Risks,
		SourceExcerpts:    contextmgr.SourceExcerpts(cliDropped, nil),
		SourceRange:       a.summarySourceRange(),
		PolicyDigest:      summarizer.Policy.PolicyDigest,
		Provider:          summarizer.Binding.Provider,
		Model:             summarizer.Binding.Model,
		EndpointAllowlist: summarizer.Policy.EndpointAllowlist,
		RedactionPolicy:   a.opts.SummaryConfig.Redaction,
		Budget:            SummaryRequestBudget(a.opts.MaxContextTokens),
		OutputLimit:       SummaryOutputLimitTokens,
	})
}

// summarySourceRange supplies the compaction's source provenance. The
// recorded preparation's range is preferred, but it is unavailable on the
// turn's first compaction: runOnceSDK discards the preparation at turn start
// and the bookkeeping Prepare that repopulates it only runs in
// ObserveRequest, which the SDK fires AFTER compactHistory has already called
// Summarize. On the PreparationManager == nil adoption row nothing ever
// records one at all. Reading the zero value there failed the request's own
// validation and silently degraded the compaction to a structural drop with
// no summary, so a range is minted from the session principal instead -
// mirroring StructuralPreparationManager.Prepare's own zero-range fallback.
func (a *sdkSummarizerAdapter) summarySourceRange() contextstate.SourceRange {
	if recorded := a.l.LastPreparation.Token.Range; recorded.Validate() == nil {
		return recorded
	}
	session := a.opts.PreparationInput.Principal.SessionID
	if session == "" {
		return a.l.LastPreparation.Token.Range
	}
	sequence := a.opts.PreparationInput.Revision.Source
	id := contextstate.SourceID{SessionID: session, Sequence: sequence}
	return contextstate.SourceRange{Start: id, End: id}
}

// compactionTokens prices one compaction: what the dropped turns (plus any
// held-aside prior summary they replace) cost, against what remains in their
// place afterwards - the rendered summary alone, or nothing when the drop was
// unsummarized. It uses the host's own message accounting with the loop's
// calibrated profile, the same semantics the PreparationManager path reports,
// so an adopted turn's compaction event and durable usage record carry real
// numbers instead of zeros.
//
// EstimateMessagesPromptCost is the total form of EstimatePromptCost: the
// only failure the latter has is marshaling tool schemas, and a compaction
// prices messages alone, so there is no error to handle here.
func (a *sdkSummarizerAdapter) compactionTokens(cliDropped, cliPrior []provider.Message, replacement provider.Message) (before, after int) {
	profile := a.l.contextAccounting()
	source := append(append([]provider.Message(nil), cliPrior...), cliDropped...)
	before = provider.EstimateMessagesPromptCost(source, 0, profile)
	if replacement.Content == "" {
		return before, 0
	}
	after = provider.EstimateMessagesPromptCost([]provider.Message{replacement}, 0, profile)
	if after > before {
		// A "compaction" that grew the prompt is not one; report the
		// conservative no-shrink shape rather than an event
		// NewCompactionEvent would reject for After > Before.
		after = before
	}
	return before, after
}

// summarizeWithOneRetry runs the governed summarizer and retries once
// on a retryable failure. The bound is exactly one retry: no loop and
// no backoff. A cancelled context stops the retry, because a retry
// would fail the same way at once.
func summarizeWithOneRetry(ctx context.Context, summarizer *contextmgr.Summarizer, request contextmgr.SummaryRequest) (contextmgr.UntrustedSummary, error) {
	summary, err := summarizer.Summarize(ctx, request)
	if err == nil || !contextmgr.RetryableSummaryFailure(err) {
		return summary, err
	}
	if ctx != nil && ctx.Err() != nil {
		return summary, err
	}
	return summarizer.Summarize(ctx, request)
}

// succeed records the pending outcome of a successful compaction,
// memoizes the call under key, and maps the host summary onto the SDK
// shape.
func (a *sdkSummarizerAdapter) succeed(cliDropped, cliPrior []provider.Message, summary contextmgr.UntrustedSummary, request contextmgr.SummaryRequest, key string) sdkplan.Summary {
	value := summary.Value()
	rendered := RenderSummaryMessage(summary, request.Input.Evidence)
	before, after := a.compactionTokens(cliDropped, cliPrior, rendered)
	a.l.sdkPendingCompaction = &sdkCompactionOutcome{
		message:    rendered,
		summarized: true,
		// rendered.Content salts the key so a compaction that drops
		// nothing new this round (a held-aside prior re-summarized
		// alone; the SDK still calls Summarize whenever a prior
		// exists, even with zero newly dropped messages) still gets
		// a non-empty, distinct key - the rendered summary is always
		// non-empty on success, unlike an empty dropped set.
		key:            sdkCompactionIdentity(cliDropped, rendered.Content),
		elidedMessages: len(cliDropped),
		elidedBytes:    sdkElidedBytes(cliDropped),
		beforeTokens:   before,
		afterTokens:    after,
	}
	a.l.summaryFailureReason = ""
	out := sdkplan.Summary{
		Objective:       value.Objective,
		State:           value.State,
		Decisions:       value.Decisions,
		Evidence:        value.Evidence,
		ChangedSurfaces: value.ChangedSurfaces,
		OpenWork:        value.OpenWork,
		Risks:           value.Risks,
	}
	a.rememberSummarize(key, out, nil)
	return out
}

// skip records the pending outcome for a real-but-unsummarized
// compaction (the dropped messages were still elided from the
// model's context even though no summary exists for them), memoizes
// the settled skip under key, and returns the wrapped skip sentinel.
// agentloop's summarizeDropped matches it through errors.Is
// regardless of the wrap.
func (a *sdkSummarizerAdapter) skip(dropped []provider.Message, reason string, key string) error {
	a.l.summaryFailureReason = reason
	// An unsummarized drop still elided real tokens and put nothing back, so
	// it is priced the same way: before = what was dropped, after = 0.
	before, after := a.compactionTokens(dropped, nil, provider.Message{})
	a.l.sdkPendingCompaction = &sdkCompactionOutcome{
		summarized: false,
		reason:     reason,
		// reason salts the key so a skip with nothing newly dropped
		// (a held-aside prior alone) still gets a non-empty key; see
		// the matching comment on the success path above.
		key:            sdkCompactionIdentity(dropped, reason),
		elidedMessages: len(dropped),
		elidedBytes:    sdkElidedBytes(dropped),
		beforeTokens:   before,
		afterTokens:    after,
	}
	err := fmt.Errorf("%w: %s", sdkplan.ErrSummarySkipped, reason)
	a.rememberSummarize(key, sdkplan.Summary{}, err)
	return err
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
// compaction from its dropped set's content plus salt, so two
// distinct compactions in the same turn ground and emit independently
// while a re-observed (already-confirmed) outcome does not re-emit.
// Mirrors compactionIdentity's role for the PM-driven path, which
// keys off a contextmgr.CommitToken that does not exist at this call
// site. salt is always non-empty at both call sites (the rendered
// summary content on success, the classified reason on a skip), so
// the returned key is empty only when dropped is also empty AND salt
// is empty - a compaction with nothing dropped and nothing to say
// about it, which never happens: the SDK calls Summarize only when
// there is something dropped or a prior to reuse, and every call
// site here passes a non-empty salt regardless. Without salt, a
// compaction whose dropped set was empty (a held-aside prior
// re-summarized alone, with nothing newly dropped this round) would
// return "", indistinguishable from "no compaction happened" to
// confirmSDKCompaction's guard, silently losing a real summary the
// model actually saw.
func sdkCompactionIdentity(dropped []provider.Message, salt string) string {
	if len(dropped) == 0 && salt == "" {
		return ""
	}
	h := fnv.New64a()
	for _, m := range dropped {
		_, _ = h.Write([]byte(m.Role))
		_, _ = h.Write([]byte(m.Content))
	}
	_, _ = h.Write([]byte(salt))
	return fmt.Sprintf("sdk:%x", h.Sum64())
}

func sdkElidedBytes(msgs []provider.Message) int {
	n := 0
	for _, m := range msgs {
		n += len(m.Content)
	}
	return n
}
