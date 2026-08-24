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
				return clonePreparedMessages(injectSummaryAfterPrepare(l, ctx, opts, fallback.Messages)), nil
			} else {
				l.PreparationErr = ferr
				return nil, ferr
			}
		}
		return nil, err
	}
	l.recordPreparation(preparation)
	l.captureOmittedEvidence(input, preparation)
	return clonePreparedMessages(injectSummaryAfterPrepare(l, ctx, opts, preparation.Messages)), nil
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
		prepared, err := prepareSDKOnce(ctx, l, opts, turn, sdkMessagesToCLI(sdkMsgs))
		if err != nil {
			return nil, err
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
