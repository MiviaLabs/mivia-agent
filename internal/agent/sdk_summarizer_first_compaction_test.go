package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	sdkplan "github.com/MiviaLabs/mivia-ai-sdk/context/plan"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
)

// newUnseededAdapterFixture mirrors newAdapterFixture but deliberately does
// NOT seed l.LastPreparation.Token.Range, reproducing real turn-start state:
// runOnceSDK calls discardPreparation before the SDK loop runs, and the
// bookkeeping Prepare that would populate the range only happens later, in
// ObserveRequest - after compactHistory has already called Summarize.
func newUnseededAdapterFixture(t *testing.T, summaryProvider contextmgr.SummaryProvider) (*sdkSummarizerAdapter, *Loop) {
	t.Helper()
	l := &Loop{
		TurnState: contextmgr.NewTurnState(),
		Messages:  []provider.Message{{Role: provider.RoleUser, Content: "the user's objective"}},
	}
	opts := Options{
		MaxContextTokens: 1000,
		SummaryConfig: SummaryConfig{
			Summarizer: ptrSummarizer(summaryInjectSummarizer(t, summaryProvider)),
			Redaction:  summaryRedaction(),
		},
	}
	opts.PreparationInput.Principal.SessionID = "sess"
	opts.PreparationInput.Revision.Source = 7
	return &sdkSummarizerAdapter{l: l, opts: opts}, l
}

// The turn's first SDK compaction must still produce a real summary. Reading
// the turn-latched LastPreparation for SourceRange made buildRequest fail
// validation ("must not be empty") on every first compaction, degrading it to
// a structural-only drop with no summary at all.
func TestSDKSummarizerSummarizesWithoutSeededPreparation(t *testing.T) {
	a, l := newUnseededAdapterFixture(t, fullSummaryProvider{})

	out, err := a.Summarize(context.Background(), sdkTestMessages())
	if err != nil {
		t.Fatalf("Summarize: %v (reason %q); the first compaction of a turn must not skip", err, l.summaryFailureReason)
	}
	if errors.Is(err, sdkplan.ErrSummarySkipped) {
		t.Fatalf("Summarize skipped: %v", err)
	}
	if out.State != "state text" {
		t.Fatalf("State = %q, want the summarizer's output", out.State)
	}
	if l.sdkPendingCompaction == nil || !l.sdkPendingCompaction.summarized {
		t.Fatalf("pending outcome = %+v, want a summarized compaction", l.sdkPendingCompaction)
	}
	if l.summaryFailureReason != "" {
		t.Fatalf("summaryFailureReason = %q, want empty", l.summaryFailureReason)
	}
}

// The minted range must be a valid, session-scoped range so the downstream
// EmitCompaction / NewCompactionEvent validation accepts it too.
func TestSDKSummarizerMintsValidSourceRange(t *testing.T) {
	a, _ := newUnseededAdapterFixture(t, fullSummaryProvider{})

	request, err := a.buildRequest(contextmgr.TurnStateSnapshot{}, sdkMessagesToCLI(sdkTestMessages()))
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if err := request.SourceRange.Validate(); err != nil {
		t.Fatalf("minted SourceRange invalid: %v", err)
	}
	if request.SourceRange.Start.SessionID != "sess" {
		t.Fatalf("SourceRange session = %q, want the principal's session", request.SourceRange.Start.SessionID)
	}
}

// A real preparation still wins: minting is only the turn-start fallback.
func TestSDKSummarizerPrefersRecordedPreparationRange(t *testing.T) {
	a, l := newAdapterFixture(t, fullSummaryProvider{})
	want := l.LastPreparation.Token.Range

	request, err := a.buildRequest(contextmgr.TurnStateSnapshot{}, sdkMessagesToCLI(sdkTestMessages()))
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if request.SourceRange != want {
		t.Fatalf("SourceRange = %+v, want the recorded preparation's range %+v", request.SourceRange, want)
	}
}

// confirmSDKCompaction grounds a synthetic Preparation that context.go latches
// for the whole turn. Without token counts, the operator banner, the bus
// event, and the durable usage record all report "0 -> 0 tokens".
func TestConfirmSDKCompactionCarriesTokenCounts(t *testing.T) {
	l := &Loop{}
	opts := Options{}
	l.sdkPendingCompaction = &sdkCompactionOutcome{
		message:        provider.Message{Role: provider.RoleUser, Content: "summary text", Name: "context-summary"},
		summarized:     true,
		key:            "sdk:tokens",
		elidedMessages: 2,
		elidedBytes:    40,
		beforeTokens:   900,
		afterTokens:    400,
	}

	confirmSDKCompaction(context.Background(), l, opts)

	if l.LastPreparation.BeforeTokens != 900 || l.LastPreparation.AfterTokens != 400 {
		t.Fatalf("tokens = %d -> %d, want 900 -> 400", l.LastPreparation.BeforeTokens, l.LastPreparation.AfterTokens)
	}
	if l.turnBeforeTokens != 900 || l.turnAfterTokens != 400 {
		t.Fatalf("turn counters = %d -> %d, want 900 -> 400", l.turnBeforeTokens, l.turnAfterTokens)
	}
}

// BeforeTokens/AfterTokens are shared fields: on non-adopted turns the
// PreparationManager fills them with WHOLE-PROMPT, calibrated,
// schema-inclusive numbers (contextmgr/planner.go), and EmitCompaction's
// banner plus the durable usage record read them without knowing which path
// produced them. Pricing only the dropped subset here would report e.g.
// "90000 -> 1200" for a context that actually went 180k -> 96k.
func TestCompactionTokensReportWholePromptScale(t *testing.T) {
	a, l := newUnseededAdapterFixture(t, fullSummaryProvider{})
	// A retained history far larger than the dropped set: whole-prompt
	// numbers must dominate, and the delta must equal the compaction's saving.
	retained := make([]provider.Message, 0, 40)
	for i := 0; i < 40; i++ {
		retained = append(retained, provider.Message{
			Role:    provider.RoleAssistant,
			Content: strings.Repeat("retained context that stays in the prompt ", 20),
		})
	}
	l.Messages = append([]provider.Message{{Role: provider.RoleUser, Content: "the user's objective"}}, retained...)
	retainedCost := provider.EstimateMessagesPromptCost(l.Messages, 0, l.contextAccounting())

	if _, err := a.Summarize(context.Background(), sdkTestMessages()); err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	pending := l.sdkPendingCompaction
	if pending == nil {
		t.Fatal("no pending outcome recorded")
	}
	if pending.beforeTokens < retainedCost {
		t.Fatalf("beforeTokens = %d, want at least the retained history cost %d: the fields carry whole-prompt totals, not the dropped subset",
			pending.beforeTokens, retainedCost)
	}
	if pending.afterTokens < retainedCost {
		t.Fatalf("afterTokens = %d, want at least the retained history cost %d: the retained prompt survives the compaction",
			pending.afterTokens, retainedCost)
	}
	if pending.afterTokens > pending.beforeTokens {
		t.Fatalf("afterTokens %d > beforeTokens %d", pending.afterTokens, pending.beforeTokens)
	}
}

// The compaction estimates must carry the loop's rolling calibration, the
// same correction contextmgr's planner applies, or the two paths' numbers are
// not comparable even once both price the whole prompt.
func TestCompactionTokensAppliesLoopCalibration(t *testing.T) {
	raw := func() int {
		a, l := newUnseededAdapterFixture(t, fullSummaryProvider{})
		if _, err := a.Summarize(context.Background(), sdkTestMessages()); err != nil {
			t.Fatalf("Summarize: %v", err)
		}
		return l.sdkPendingCompaction.beforeTokens
	}()

	a, l := newUnseededAdapterFixture(t, fullSummaryProvider{})
	// A provider that bills twice the estimate: the calibrator's ratio must
	// scale the reported compaction numbers by the same factor.
	l.Calibration.Update(100, 200)
	if l.Calibration.Samples == 0 {
		t.Fatal("calibration fixture did not record a sample")
	}
	if _, err := a.Summarize(context.Background(), sdkTestMessages()); err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	got := l.sdkPendingCompaction.beforeTokens
	if got <= raw {
		t.Fatalf("beforeTokens = %d with a %.2f calibration ratio, want more than the uncalibrated %d",
			got, l.Calibration.Ratio, raw)
	}
}

// The adapter is what knows the compaction's real sizes, so it must park them
// on the pending outcome for confirmSDKCompaction to ground.
func TestSDKSummarizerRecordsTokenCountsOnPendingOutcome(t *testing.T) {
	a, l := newUnseededAdapterFixture(t, fullSummaryProvider{})

	if _, err := a.Summarize(context.Background(), sdkTestMessages()); err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	pending := l.sdkPendingCompaction
	if pending == nil {
		t.Fatal("no pending outcome recorded")
	}
	if pending.beforeTokens <= 0 {
		t.Fatalf("beforeTokens = %d, want a positive estimate of the pre-compaction history", pending.beforeTokens)
	}
	// NewCompactionEvent rejects After > Before, so that ordering is the
	// invariant; the fixture's messages are too small for the summary to be
	// strictly cheaper than what it replaced.
	if pending.afterTokens < 0 || pending.afterTokens > pending.beforeTokens {
		t.Fatalf("afterTokens = %d, want 0 <= after <= beforeTokens %d", pending.afterTokens, pending.beforeTokens)
	}
}

var _ = sdkshape.Message{}
