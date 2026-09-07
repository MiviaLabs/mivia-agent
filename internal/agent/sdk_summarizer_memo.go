package agent

import (
	"fmt"
	"hash/fnv"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	sdkplan "github.com/MiviaLabs/mivia-ai-sdk/context/plan"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
)

// sdkSummaryMemo holds one sdkSummarizerAdapter.Summarize result for
// replay inside the same turn. It caches the returned summary, the
// returned error, the failure reason the call recorded on the Loop,
// and the pending compaction outcome, so a replay reproduces the
// whole call byte for byte with no new provider request.
//
// Only a settled outcome is memoized: a success, or a non-retryable
// skip. A retryable failure is never memoized, so the SDK's own
// recovery may still attempt the compaction again.
type sdkSummaryMemo struct {
	key     string
	summary sdkplan.Summary
	err     error
	reason  string
	outcome sdkCompactionOutcome
}

// sdkSummarizeInputKey derives the memo key from the INPUT of one
// Summarize call: the held-aside prior summary, when present, and the
// dropped messages. The key cannot reuse sdkCompactionIdentity, whose
// salt is the rendered summary - that text exists only after the call
// returns, so it cannot identify the call before it runs. Role and
// content of every input message feed the hash; a marker separates
// the prior from the dropped set, so a prior with content "x" and a
// dropped message with content "x" never collide.
func sdkSummarizeInputKey(prior *sdkshape.Message, dropped []provider.Message) string {
	h := fnv.New64a()
	if prior != nil {
		_, _ = h.Write([]byte("prior\x00"))
		_, _ = h.Write([]byte(prior.Role))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(prior.Content))
		_, _ = h.Write([]byte{0})
	}
	_, _ = h.Write([]byte("dropped\x00"))
	for _, m := range dropped {
		_, _ = h.Write([]byte(m.Role))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(m.Content))
		_, _ = h.Write([]byte{0})
	}
	return fmt.Sprintf("sdkin:%x", h.Sum64())
}

// memoized returns the memo for key, or nil when this turn holds no
// settled result for that input.
func (a *sdkSummarizerAdapter) memoized(key string) *sdkSummaryMemo {
	memo := a.l.sdkSummaryMemo
	if memo == nil || memo.key != key {
		return nil
	}
	return memo
}

// replayMemo reproduces a memoized call: it restores the Loop state
// the original call recorded and returns the original result. The
// pending outcome is re-stashed so a confirming observer call still
// grounds the compaction; confirmSDKCompaction's own key guard stops
// a re-observed outcome from emitting twice.
func (a *sdkSummarizerAdapter) replayMemo(memo *sdkSummaryMemo) (sdkplan.Summary, error) {
	a.l.summaryFailureReason = memo.reason
	outcome := memo.outcome
	a.l.sdkPendingCompaction = &outcome
	return memo.summary, memo.err
}

// rememberSummarize stores one settled Summarize result under key.
// It reads the outcome and the reason from the Loop, so the memo
// always replays exactly what the call recorded.
func (a *sdkSummarizerAdapter) rememberSummarize(key string, summary sdkplan.Summary, err error) {
	memo := &sdkSummaryMemo{
		key:     key,
		summary: summary,
		err:     err,
		reason:  a.l.summaryFailureReason,
	}
	if a.l.sdkPendingCompaction != nil {
		memo.outcome = *a.l.sdkPendingCompaction
	}
	a.l.sdkSummaryMemo = memo
}
