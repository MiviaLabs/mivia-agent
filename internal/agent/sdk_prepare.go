package agent

import (
	"context"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
)

// prepareSDKOnce runs ONE host-side preparation pass over msgs and
// returns the prepared CLI messages. It is the per-iteration body the
// SDK Trim closure calls (the legacy path runs the same body once per
// step in prepareStep). Unlike prepareStep it never assigns
// l.Messages: the SDK owns the history variable, and the Trim return
// value replaces it. Failure identity, the interrupted-context
// fallback, and the PreparationErr bookkeeping match context.go's
// prepareStep exactly.
func prepareSDKOnce(ctx context.Context, l *Loop, opts Options, turn *sdkTurnState, msgs []provider.Message) ([]provider.Message, error) {
	toolSpecs := l.initialToolSpecs(opts)
	if turn != nil {
		if adv := turn.currentAdvertised(); adv != nil {
			toolSpecs = adv
		}
	}
	input := l.buildPrepareInput(toolSpecs, opts)
	input.Messages = msgs
	preparation, err := opts.PreparationManager.Prepare(ctx, input)
	if err != nil {
		l.PreparationErr = err
		if !l.HasPreparation && opts.WorkLimits.DeadlineAt.IsZero() && interruptedContext(ctx, err) {
			if fallback, ferr := opts.PreparationManager.Prepare(context.Background(), input); ferr == nil {
				l.recordPreparation(fallback)
				l.captureOmittedEvidence(input, fallback)
				l.PreparationErr = nil
				preparedWithSummary := injectSummaryAfterPrepare(l, ctx, opts, fallback.Messages)
				if fallback.Compacted {
					key := compactionIdentity(fallback.Token)
					if key != "" && key != l.lastEmittedCompactionKey {
						l.lastEmittedCompactionKey = key
						_, haveSummary := l.InjectedSummary()
						reason := l.SummaryFailureReason()
						if !haveSummary && reason == "" && opts.SummaryConfig.Summarizer == nil {
							if opts.SummaryConfig.UnavailableReason != "" {
								reason = opts.SummaryConfig.UnavailableReason
							} else {
								reason = "no summarizer is configured for this session"
							}
						}
						EmitCompaction(ctx, opts, fallback, haveSummary, reason)
						l.turnCompactionEmitted = true
					}
				}
				return clonePreparedMessages(preparedWithSummary), nil
			} else {
				l.PreparationErr = ferr
				return nil, ferr
			}
		}
		return nil, err
	}
	l.recordPreparation(preparation)
	l.captureOmittedEvidence(input, preparation)
	preparedWithSummary := injectSummaryAfterPrepare(l, ctx, opts, preparation.Messages)
	if preparation.Compacted {
		key := compactionIdentity(preparation.Token)
		if key != "" && key != l.lastEmittedCompactionKey {
			l.lastEmittedCompactionKey = key
			_, haveSummary := l.InjectedSummary()
			reason := l.SummaryFailureReason()
			if !haveSummary && reason == "" && opts.SummaryConfig.Summarizer == nil {
				if opts.SummaryConfig.UnavailableReason != "" {
					reason = opts.SummaryConfig.UnavailableReason
				} else {
					reason = "no summarizer is configured for this session"
				}
			}
			EmitCompaction(ctx, opts, preparation, haveSummary, reason)
			l.turnCompactionEmitted = true
		}
	}
	return clonePreparedMessages(preparedWithSummary), nil
}

// sdkPrepareTrim builds the SDK Options.Trim closure that runs the
// host-side preparation before EVERY Completer call, including the
// first, so mid-turn elision and the turn compaction counters fire
// with legacy per-step fidelity. The SDK applies the returned slice
// as the run's history (and Result.History). A nil PreparationManager
// returns nil: no Trim, the raw history passes through, exactly the
// pre-Trim behavior. The SDK's Window stays nil so the SDK never runs
// its own planning pass on top of the host's.
func sdkPrepareTrim(l *Loop, opts Options, turn *sdkTurnState) func(context.Context, []sdkshape.Message) ([]sdkshape.Message, error) {
	if opts.PreparationManager == nil {
		return nil
	}
	return func(ctx context.Context, sdkMsgs []sdkshape.Message) ([]sdkshape.Message, error) {
		// Since the ContinueOnStop hook (mivia-ai-sdk v0.1.3) drives the
		// empty-response retry inside one loop, the triggering empty
		// assistant message stays in the run history: the SDK appends
		// resp.Message before the stop decision. The wire layers drop that
		// shape on every provider request (toAPIMessages,
		// anthropic_request.go), but the message-shape validation inside
		// preparation hard-rejects it first, so it must be gone before
		// prepareSDKOnce sees it. DropEmptyAssistantTurns is this repo's
		// repair for exactly the shape, applied here at the validation seam.
		cliMsgs := provider.DropEmptyAssistantTurns(sdkMessagesToCLI(sdkMsgs))
		prepared, err := prepareSDKOnce(ctx, l, opts, turn, cliMsgs)
		if err != nil {
			return nil, err
		}
		// The prepared slice IS the request: post-pruning, post-compaction,
		// exactly what the provider is about to be billed for. Reported here
		// so a host can describe the live context at every step instead of
		// waiting for the turn to end and adopt.
		if opts.ObserveRequestHistory != nil {
			opts.ObserveRequestHistory(prepared)
		}
		return cliMessagesToSDK(prepared), nil
	}
}

// injectSummaryAfterPrepare runs the CLI's host-side summary inject
// on a freshly prepared messages slice and returns the slice with the
// summary message appended as the last user-role frame. A nil
// summarizer or a non-compacted preparation returns the messages
// unchanged, mirroring injectSummary's structural-only fallback.
func injectSummaryAfterPrepare(l *Loop, ctx context.Context, opts Options, prepared []provider.Message) []provider.Message {
	if opts.SummaryConfig.Summarizer == nil || !l.HasPreparation || !l.LastPreparation.Compacted {
		return prepared
	}
	// Temporarily swap l.Messages to the prepared slice so injectSummary
	// reads the compacted history instead of the live one, then restore.
	// This matches what loop.go:332 does in the legacy path: prepareStep
	// overwrites l.Messages from preparation.Messages before
	// injectSummary sees them.
	original := l.Messages
	l.Messages = prepared
	defer func() { l.Messages = original }()
	return l.injectSummary(ctx, opts)
}

// sdkInitialHistory returns the SDK run's starting history. Preparation
// runs per iteration through Trim; the ONLY up-front pass is a
// pre-canceled ctx, where the SDK would bail at its loop-top check
// before Trim ever runs and the interrupted-preparation recovery
// identity would never surface (the turn commit would misreport a
// checkpoint conflict). A live ctx defers entirely to Trim.
func sdkInitialHistory(ctx context.Context, l *Loop, opts Options, msgs []provider.Message) ([]sdkshape.Message, error) {
	if opts.PreparationManager == nil || ctx.Err() == nil {
		return cliMessagesToSDK(msgs), nil
	}
	prepared, perr := prepareSDKOnce(ctx, l, opts, nil, msgs)
	if perr != nil {
		return nil, perr
	}
	return cliMessagesToSDK(prepared), nil
}
