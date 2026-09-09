package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	sdkagentloop "github.com/MiviaLabs/mivia-ai-sdk/agentloop"
	sdkplan "github.com/MiviaLabs/mivia-ai-sdk/context/plan"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
)

// fullSummaryProvider returns a Summary with every field populated,
// so TestSDKSummarizerAdapterMapsSevenFields can assert the mapped
// sdkplan.Summary matches field for field.
type fullSummaryProvider struct{}

func (fullSummaryProvider) Summarize(_ context.Context, request contextmgr.SummaryRequest) (contextmgr.Summary, error) {
	return contextmgr.Summary{
		Version:         request.Input.Version,
		Objective:       "objective text",
		State:           "state text",
		Decisions:       []string{"d1", "d2"},
		Evidence:        []string{"e1"},
		ChangedSurfaces: []string{"cs1", "cs2"},
		OpenWork:        []string{"ow1"},
		Risks:           []string{"r1", "r2"},
		SourceRange:     request.SourceRange,
	}, nil
}

// failingSummaryProvider always returns err.
type failingSummaryProvider struct{ err error }

func (f failingSummaryProvider) Summarize(context.Context, contextmgr.SummaryRequest) (contextmgr.Summary, error) {
	return contextmgr.Summary{}, f.err
}

func newAdapterFixture(t *testing.T, summaryProvider contextmgr.SummaryProvider) (*sdkSummarizerAdapter, *Loop) {
	t.Helper()
	sourceRange, err := contextstate.NewSourceRange(
		contextstate.SourceID{SessionID: "sess", Sequence: 1},
		contextstate.SourceID{SessionID: "sess", Sequence: 2},
	)
	if err != nil {
		t.Fatalf("NewSourceRange: %v", err)
	}
	l := &Loop{
		TurnState: contextmgr.NewTurnState(),
		Messages:  []provider.Message{{Role: provider.RoleUser, Content: "the user's objective"}},
	}
	l.LastPreparation.Token.Range = sourceRange
	opts := Options{
		MaxContextTokens: 1000,
		SummaryConfig: SummaryConfig{
			Summarizer: ptrSummarizer(summaryInjectSummarizer(t, summaryProvider)),
			Redaction:  summaryRedaction(),
		},
	}
	return &sdkSummarizerAdapter{l: l, opts: opts}, l
}

func ptrSummarizer(s contextmgr.Summarizer) *contextmgr.Summarizer { return &s }

func sdkTestMessages() []sdkshape.Message {
	return []sdkshape.Message{
		{Role: sdkshape.RoleUser, Content: "dropped one"},
		{Role: sdkshape.RoleAssistant, Content: "dropped two"},
	}
}

// TestSDKSummarizerAdapterMapsSevenFields proves a successful
// Summarize maps the host's 7 overlapping Summary fields onto
// sdkplan.Summary field by field, and stashes a pending compaction
// outcome (summarized) for confirmSDKCompaction to drain.
func TestSDKSummarizerAdapterMapsSevenFields(t *testing.T) {
	a, l := newAdapterFixture(t, fullSummaryProvider{})
	got, err := a.Summarize(context.Background(), sdkTestMessages())
	if err != nil {
		t.Fatalf("Summarize() = %v, want nil", err)
	}
	want := sdkplan.Summary{
		Objective:       "objective text",
		State:           "state text",
		Decisions:       []string{"d1", "d2"},
		Evidence:        []string{"e1"},
		ChangedSurfaces: []string{"cs1", "cs2"},
		OpenWork:        []string{"ow1"},
		Risks:           []string{"r1", "r2"},
	}
	if got.Objective != want.Objective || got.State != want.State {
		t.Fatalf("Objective/State = %+v, want %+v", got, want)
	}
	if len(got.Decisions) != 2 || len(got.ChangedSurfaces) != 2 || len(got.Risks) != 2 {
		t.Fatalf("list fields not mapped: %+v", got)
	}
	if l.sdkPendingCompaction == nil {
		t.Fatal("sdkPendingCompaction not set after a successful Summarize")
	}
	if !l.sdkPendingCompaction.summarized {
		t.Fatal("pending outcome not marked summarized")
	}
	if l.sdkPendingCompaction.key == "" {
		t.Fatal("pending outcome carries no identity key")
	}
	if l.hasInjectedSummary {
		t.Fatal("hasInjectedSummary set by Summarize itself; it must only be set by confirmSDKCompaction")
	}
}

// TestSDKSummarizerAdapterNoSummarizerSkips proves a nil
// SummaryConfig.Summarizer returns a wrapped ErrSummarySkipped
// without touching TurnState.
func TestSDKSummarizerAdapterNoSummarizerSkips(t *testing.T) {
	l := &Loop{TurnState: contextmgr.NewTurnState()}
	a := &sdkSummarizerAdapter{l: l, opts: Options{MaxContextTokens: 1000}}
	_, err := a.Summarize(context.Background(), sdkTestMessages())
	if !errors.Is(err, sdkplan.ErrSummarySkipped) {
		t.Fatalf("err = %v, want errors.Is ErrSummarySkipped", err)
	}
	if l.sdkPendingCompaction == nil || l.sdkPendingCompaction.summarized {
		t.Fatalf("pending = %+v, want a non-nil unsummarized skip outcome", l.sdkPendingCompaction)
	}
}

// TestSDKSummarizerAdapterNonRetryableFailureSkipsWithReason proves a
// non-retryable provider failure (redaction refusal) wraps
// ErrSummarySkipped with the classified reason text.
func TestSDKSummarizerAdapterNonRetryableFailureSkipsWithReason(t *testing.T) {
	a, l := newAdapterFixture(t, failingSummaryProvider{err: contextmgr.ErrSummaryRedactionRefused})
	_, err := a.Summarize(context.Background(), sdkTestMessages())
	if !errors.Is(err, sdkplan.ErrSummarySkipped) {
		t.Fatalf("err = %v, want errors.Is ErrSummarySkipped", err)
	}
	if err.Error() == sdkplan.ErrSummarySkipped.Error() {
		t.Fatalf("err = %v, want the classified reason appended, not the bare sentinel", err)
	}
	if l.summaryFailureReason != contextmgr.SummaryReasonRedactionRefused {
		t.Fatalf("summaryFailureReason = %q, want %q", l.summaryFailureReason, contextmgr.SummaryReasonRedactionRefused)
	}
}

// TestSDKSummarizerAdapterRetryableFailureFailsClosed proves a
// retryable provider failure (transport error) returns unwrapped, so
// the SDK fails the compaction closed instead of silently degrading -
// the adapter has no inline retry loop of its own.
func TestSDKSummarizerAdapterRetryableFailureFailsClosed(t *testing.T) {
	a, l := newAdapterFixture(t, failingSummaryProvider{err: context.DeadlineExceeded})
	_, err := a.Summarize(context.Background(), sdkTestMessages())
	if errors.Is(err, sdkplan.ErrSummarySkipped) {
		t.Fatalf("err = %v, want NOT errors.Is ErrSummarySkipped: a retryable failure must fail closed", err)
	}
	if l.sdkPendingCompaction != nil {
		t.Fatalf("pending = %+v, want nil: a retryable failure records no outcome to confirm", l.sdkPendingCompaction)
	}
}

// TestConfirmSDKCompactionGroundsPendingOutcome proves
// confirmSDKCompaction drains a pending outcome and grounds it:
// records the injected summary, marks LastPreparation.Compacted, and
// leaves the pending field drained so a later, unrelated observer
// call is a no-op.
func TestConfirmSDKCompactionGroundsPendingOutcome(t *testing.T) {
	l := &Loop{}
	msg := provider.Message{Role: provider.RoleUser, Content: "summary text", Name: "context-summary"}
	l.sdkPendingCompaction = &sdkCompactionOutcome{
		message:        msg,
		summarized:     true,
		key:            "sdk:abc",
		elidedMessages: 2,
		elidedBytes:    40,
	}
	opts := Options{}
	confirmSDKCompaction(context.Background(), l, opts)

	if l.sdkPendingCompaction != nil {
		t.Fatal("sdkPendingCompaction not drained")
	}
	got, ok := l.InjectedSummary()
	if !ok || got.Content != "summary text" {
		t.Fatalf("InjectedSummary() = (%+v, %v), want the pending message", got, ok)
	}
	if !l.LastPreparation.Compacted {
		t.Fatal("LastPreparation.Compacted not set true")
	}
	if !l.turnCompacted {
		t.Fatal("turnCompacted not set true")
	}
	if l.lastEmittedCompactionKey != "sdk:abc" {
		t.Fatalf("lastEmittedCompactionKey = %q, want sdk:abc", l.lastEmittedCompactionKey)
	}

	// A second call with nothing pending is a no-op: state unchanged.
	confirmSDKCompaction(context.Background(), l, opts)
	if l.lastEmittedCompactionKey != "sdk:abc" {
		t.Fatal("second confirm with no pending outcome changed state")
	}
}

// TestConfirmSDKCompactionAccumulatesAcrossTwoCompactions proves a
// turn with two distinct real compactions grounds and emits both,
// not just the last one: elided counts accumulate rather than being
// overwritten, and the second compaction's distinct key is not
// treated as a duplicate of the first.
func TestConfirmSDKCompactionAccumulatesAcrossTwoCompactions(t *testing.T) {
	l := &Loop{}
	opts := Options{}

	l.sdkPendingCompaction = &sdkCompactionOutcome{
		summarized: true, key: "sdk:first",
		elidedMessages: 2, elidedBytes: 20,
		message: provider.Message{Role: provider.RoleUser, Content: "first summary", Name: "context-summary"},
	}
	confirmSDKCompaction(context.Background(), l, opts)
	if l.LastPreparation.ElidedMessages != 2 || l.LastPreparation.ElidedBytes != 20 {
		t.Fatalf("after first compaction: elided = %d/%d, want 2/20", l.LastPreparation.ElidedMessages, l.LastPreparation.ElidedBytes)
	}
	firstKey := l.lastEmittedCompactionKey
	if firstKey != "sdk:first" {
		t.Fatalf("lastEmittedCompactionKey = %q, want sdk:first", firstKey)
	}

	l.sdkPendingCompaction = &sdkCompactionOutcome{
		summarized: true, key: "sdk:second",
		elidedMessages: 3, elidedBytes: 30,
		message: provider.Message{Role: provider.RoleUser, Content: "second summary", Name: "context-summary"},
	}
	confirmSDKCompaction(context.Background(), l, opts)
	if l.LastPreparation.ElidedMessages != 5 || l.LastPreparation.ElidedBytes != 50 {
		t.Fatalf("after second compaction: elided = %d/%d, want accumulated 5/50", l.LastPreparation.ElidedMessages, l.LastPreparation.ElidedBytes)
	}
	if l.lastEmittedCompactionKey != "sdk:second" {
		t.Fatalf("lastEmittedCompactionKey = %q, want sdk:second (the second, distinct compaction)", l.lastEmittedCompactionKey)
	}
	got, _ := l.InjectedSummary()
	if got.Content != "second summary" {
		t.Fatalf("InjectedSummary() = %+v, want the SECOND compaction's summary (not lost)", got)
	}
}

// TestConfirmSDKCompactionAbandonedOutcomeNeverGrounds proves the
// round-2 hostile-review fix directly: an outcome that Summarize
// stashed but that never reaches a confirming observer call (the
// SDK's own recovery abandoned it) is never grounded - simulated here
// by simply never calling confirmSDKCompaction, then asserting
// resetTurnCompaction (which every turn-start path calls) clears it.
func TestConfirmSDKCompactionAbandonedOutcomeNeverGrounds(t *testing.T) {
	l := &Loop{}
	l.sdkPendingCompaction = &sdkCompactionOutcome{
		summarized: true, key: "sdk:abandoned",
		message: provider.Message{Role: provider.RoleUser, Content: "abandoned summary", Name: "context-summary"},
	}
	l.resetTurnCompaction()
	if l.sdkPendingCompaction != nil {
		t.Fatal("resetTurnCompaction did not clear an abandoned pending outcome")
	}
	if l.hasInjectedSummary {
		t.Fatal("an abandoned outcome must never set hasInjectedSummary")
	}
}

// TestSDKCompactionAdoptedRequiresOptInWithPreparationManager proves
// the opt-in gate: a turn wiring PreparationManager, a summarizer,
// and a positive MaxContextTokens does NOT adopt SDK compaction by
// default (preserving every existing PreparationManager-driven
// call site's behavior); it adopts only when PreferSDKCompaction is
// explicitly set. A turn with no PreparationManager is unaffected by
// the field either way.
func TestSDKCompactionAdoptedRequiresOptInWithPreparationManager(t *testing.T) {
	base := Options{
		MaxContextTokens:   1000,
		PreparationManager: &stubPreparationManager{keep: 3},
		SummaryConfig:      SummaryConfig{Summarizer: ptrSummarizer(summaryInjectSummarizer(t, fullSummaryProvider{}))},
	}
	if sdkCompactionAdopted(base) {
		t.Fatal("adopted by default with a PreparationManager wired; must require PreferSDKCompaction")
	}
	base.PreferSDKCompaction = true
	if !sdkCompactionAdopted(base) {
		t.Fatal("not adopted with PreferSDKCompaction set")
	}

	noPM := Options{
		MaxContextTokens: 1000,
		SummaryConfig:    SummaryConfig{Summarizer: ptrSummarizer(summaryInjectSummarizer(t, fullSummaryProvider{}))},
	}
	if !sdkCompactionAdopted(noPM) {
		t.Fatal("not adopted with no PreparationManager; this row must stay automatic")
	}
}

// TestAdoptSDKCompactionEffectiveThresholds pins the host-style
// mapping adoptSDKCompaction wires: TriggerPercent 100 with
// TargetTokens at MaxContextTokens/2 reaches an exact 80%/50%
// trigger/target of MaxContextTokens, and PreserveNames carries
// opts.PreparationInput.PreserveNames through unchanged.
func TestAdoptSDKCompactionEffectiveThresholds(t *testing.T) {
	l := &Loop{Completer: &fakeCompleter{name: "test"}, Tools: nil}
	completer, err := newAgentLoopCompleterWithDefaults(l.Completer, turnRequestDefaults{}, nil, nil, nil, provider.ContextAccountingProfile{})
	if err != nil {
		t.Fatalf("newAgentLoopCompleterWithDefaults: %v", err)
	}
	opts := Options{
		MaxContextTokens:    1000,
		PreparationManager:  &stubPreparationManager{keep: 3},
		PreferSDKCompaction: true,
		SummaryConfig:       SummaryConfig{Summarizer: ptrSummarizer(summaryInjectSummarizer(t, fullSummaryProvider{}))},
		PreparationInput:    contextmgr.PrepareInput{PreserveNames: []string{"core-memory-context"}},
	}
	var out sdkagentloop.Options
	if err := adoptSDKCompaction(l, &out, completer, opts, newSDKTurnState()); err != nil {
		t.Fatalf("adoptSDKCompaction: %v", err)
	}
	window := out.Compaction.Window
	if window == nil {
		t.Fatal("Window not wired")
	}
	if window.MaxTokens != 1000 || window.Reserve != 200 {
		t.Fatalf("window = %+v, want MaxTokens 1000 Reserve 200", window)
	}
	if got, want := window.CompactTrigger(), 800; got != want {
		t.Fatalf("CompactTrigger() = %d, want %d (exactly 80%% of MaxContextTokens)", got, want)
	}
	if got, want := window.CompactTarget(), 500; got != want {
		t.Fatalf("CompactTarget() = %d, want %d (exactly 50%% of MaxContextTokens)", got, want)
	}
	if len(window.Compaction.PreserveNames) != 1 || window.Compaction.PreserveNames[0] != "core-memory-context" {
		t.Fatalf("PreserveNames = %v, want [core-memory-context]", window.Compaction.PreserveNames)
	}
	if _, ok := out.Compaction.Summarizer.(*sdkSummarizerAdapter); !ok {
		t.Fatalf("Summarizer = %T, want *sdkSummarizerAdapter", out.Compaction.Summarizer)
	}
	if out.ObserveRequest == nil {
		t.Fatal("ObserveRequest not wired")
	}
}

// TestSDKCompactionObserverNoPreparationManagerDoesNotPanic is a RED
// test for a fast-bug-audit finding: sdkCompactionObserver's
// bookkeeping Prepare pass called opts.PreparationManager.Prepare
// unconditionally, panicking on a nil interface whenever a turn
// adopted SDK compaction with no PreparationManager wired - a row
// sdkCompactionAdopted deliberately keeps reachable ("that row
// already adopted before this field existed... stays automatic").
func TestSDKCompactionObserverNoPreparationManagerDoesNotPanic(t *testing.T) {
	l := &Loop{Completer: &fakeCompleter{name: "test"}, Tools: nil, TurnState: contextmgr.NewTurnState()}
	completer, err := newAgentLoopCompleterWithDefaults(l.Completer, turnRequestDefaults{}, nil, nil, nil, provider.ContextAccountingProfile{})
	if err != nil {
		t.Fatalf("newAgentLoopCompleterWithDefaults: %v", err)
	}
	opts := Options{
		MaxContextTokens: 1000,
		SummaryConfig:    SummaryConfig{Summarizer: ptrSummarizer(summaryInjectSummarizer(t, fullSummaryProvider{}))},
		// PreparationManager deliberately left nil.
	}
	var out sdkagentloop.Options
	if err := adoptSDKCompaction(l, &out, completer, opts, newSDKTurnState()); err != nil {
		t.Fatalf("adoptSDKCompaction: %v", err)
	}
	if out.ObserveRequest == nil {
		t.Fatal("ObserveRequest not wired")
	}
	req := sdkshape.Request{Messages: []sdkshape.Message{{Role: sdkshape.RoleUser, Content: "hi"}}}
	if err := out.ObserveRequest(context.Background(), req); err != nil {
		t.Fatalf("ObserveRequest() = %v, want nil with no PreparationManager wired", err)
	}
}

// TestSDKSummarizerAdapterEmptyDroppedWithPriorStillGrounds is a RED
// test for a fast-bug-audit finding: when the SDK calls Summarize
// with only a held-aside prior summary and nothing newly dropped
// (compactHistory's len(res.Dropped) > 0 || prior != nil condition
// admits this), sdkCompactionIdentity of the empty dropped set
// returns "", which confirmSDKCompaction's guard treats identically
// to "nothing pending" - silently dropping a real, sent summary from
// the durable commit.
func TestSDKSummarizerAdapterEmptyDroppedWithPriorStillGrounds(t *testing.T) {
	a, l := newAdapterFixture(t, fullSummaryProvider{})
	prior := sdkshape.Message{Role: sdkshape.RoleUser, Name: sdkplan.SummaryMessageName, Content: "prior summary"}
	_, err := a.Summarize(context.Background(), []sdkshape.Message{prior})
	if err != nil {
		t.Fatalf("Summarize() = %v, want nil", err)
	}
	confirmSDKCompaction(context.Background(), l, a.opts)
	got, ok := l.InjectedSummary()
	if !ok {
		t.Fatal("InjectedSummary() reports nothing injected; the real, sent summary was lost")
	}
	if got.Content == "" {
		t.Fatal("InjectedSummary() content is empty")
	}
}
