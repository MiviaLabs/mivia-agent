package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// capturingSummaryProvider records every summary request and returns a short
// validated summary (never echoing oversized host fields back, so the token
// estimate stays inside the request OutputLimit).
type capturingSummaryProvider struct {
	requests []contextmgr.SummaryRequest
	err      error
}

func (p *capturingSummaryProvider) Summarize(_ context.Context, request contextmgr.SummaryRequest) (contextmgr.Summary, error) {
	p.requests = append(p.requests, request)
	if p.err != nil {
		return contextmgr.Summary{}, p.err
	}
	return contextmgr.Summary{
		Version:     request.Input.Version,
		Objective:   "summarized objective",
		State:       request.Input.State,
		SourceRange: request.SourceRange,
	}, nil
}

// summaryInjectPolicy is the captured policy the fake Summarizer requires:
// summary and network explicitly enabled, a credential scope, an endpoint
// allowlist, and a policy digest.
func summaryInjectPolicy() contextstate.PolicySnapshot {
	return contextstate.PolicySnapshot{
		SummaryEnabled: true, RedactionConfigured: true, Provider: "summary-test", Model: "model",
		CredentialScope: "scope", NetworkEnabled: true,
		EndpointAllowlist: []string{"https://summary.invalid"},
		PolicyDigest:      strings.Repeat("a", 64),
	}
}

func summaryInjectSummarizer(t *testing.T, provider contextmgr.SummaryProvider) contextmgr.Summarizer {
	t.Helper()
	binding, err := contextstate.NewBindingRevision("summary-test", "model", 1)
	if err != nil {
		t.Fatal(err)
	}
	summarizer, err := contextmgr.NewSummarizer(provider, binding, summaryInjectPolicy())
	if err != nil {
		t.Fatal(err)
	}
	return summarizer
}

func summaryRedaction() contextstate.RedactionPolicy {
	return contextstate.RedactionPolicy{Configured: true, Patterns: []string{"never-match"}}
}

// compactingPreparationProbe returns a compacted preparation whose token range
// and budget accounting are valid, so the loop's summary path can build a
// request. compacted=false exercises the non-compacting turn.
type compactingPreparationProbe struct {
	compacted bool
	calls     int
}

func (p *compactingPreparationProbe) Prepare(_ context.Context, input contextmgr.PrepareInput) (contextmgr.Preparation, error) {
	p.calls++
	rangeValue := contextstate.SourceRange{
		Start: contextstate.SourceID{SessionID: input.Principal.SessionID, Sequence: input.Revision.Source},
		End:   contextstate.SourceID{SessionID: input.Principal.SessionID, Sequence: input.Revision.Source},
	}
	prep, err := contextmgr.CapturePreparation(input, contextmgr.CheckpointCandidate{
		SourceRange: rangeValue, ActiveContext: []byte("active"),
	}, input.Messages, p.compacted, "summary-inject-test")
	if err != nil {
		return contextmgr.Preparation{}, err
	}
	prep.BeforeTokens = 1000
	prep.AfterTokens = 400
	return prep, nil
}

func (p *compactingPreparationProbe) Discard(contextmgr.Preparation) {}

func summaryProbeOptions(t *testing.T, summarizer *contextmgr.Summarizer, probe contextmgr.PreparationManager, maxContextTokens int) Options {
	t.Helper()
	principal, err := contextstate.NewPrincipal("workspace", "session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := contextstate.NewBindingRevision("summary-test", "model", 1)
	if err != nil {
		t.Fatal(err)
	}
	return Options{Backend: "legacy",
		Model: "model", MaxContextTokens: maxContextTokens, MaxSteps: 5,
		PreparationManager: probe,
		PreparationInput: contextmgr.PrepareInput{
			Budget: maxContextTokens, Principal: principal, Binding: binding,
			Revision: contextstate.Revision{Session: 1, Durable: 1, Source: 1},
		},
		SummaryConfig: SummaryConfig{
			Summarizer: summarizer,
			Redaction:  summaryRedaction(),
		},
	}
}

// capturingRequestCompleter records every request the loop sends. With
// toolStep=true it emits one write_file call on the first step, then a final
// answer, so the turn-state tracker has tool facts before the second request.
type capturingRequestCompleter struct {
	requests []provider.Request
	toolStep bool
	step     int
}

func (c *capturingRequestCompleter) Name() string { return "capture" }
func (c *capturingRequestCompleter) Chat(context.Context, provider.Request) (string, error) {
	return "answer", nil
}
func (c *capturingRequestCompleter) ChatStream(context.Context, provider.Request, io.Writer) (string, error) {
	return "answer", nil
}
func (c *capturingRequestCompleter) ChatTurn(_ context.Context, req provider.Request) (*provider.Response, error) {
	c.requests = append(c.requests, req)
	c.step++
	if c.toolStep && c.step == 1 {
		call := provider.ToolCall{ID: "t1", Type: "function"}
		call.Function.Name = "write_file"
		call.Function.Arguments = `{}`
		return &provider.Response{ToolCalls: []provider.ToolCall{call}, FinishReason: "tool_calls"}, nil
	}
	return &provider.Response{Content: "answer", FinishReason: "stop"}, nil
}

// writeFakeTool is a write-class tool so the turn-state tracker records a
// changed surface for its calls.
type writeFakeTool struct{ name string }

func (f *writeFakeTool) Name() string               { return f.name }
func (f *writeFakeTool) Description() string        { return "fake write tool for testing" }
func (f *writeFakeTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (f *writeFakeTool) Capability(_ json.RawMessage) tools.Capability {
	return tools.Capability{Class: tools.ExecutionWrite, ResourceKey: "workspace:mutation"}
}
func (f *writeFakeTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return "ok", nil
}

func findSummaryMessage(messages []provider.Message) (provider.Message, bool) {
	for _, message := range messages {
		if message.Name == SummaryMessageName {
			return message, true
		}
	}
	return provider.Message{}, false
}

func anyRequestCarriesSummary(requests []provider.Request) bool {
	for _, request := range requests {
		if _, ok := findSummaryMessage(request.Messages); ok {
			return true
		}
	}
	return false
}

// TestSummaryInjectionSentRequestCarriesSummary is the agent-loop Phase 2
// proof: after a compacted preparation, the request actually sent to the
// completer carries the context-summary message.
func TestSummaryInjectionSentRequestCarriesSummary(t *testing.T) {
	summ := &capturingSummaryProvider{}
	summarizer := summaryInjectSummarizer(t, summ)
	completer := &capturingRequestCompleter{}
	loop := &Loop{Completer: completer, Tools: tools.NewRegistry()}
	if _, err := loop.Run(context.Background(), "question", summaryProbeOptions(t, &summarizer, &compactingPreparationProbe{compacted: true}, 100_000)); err != nil {
		t.Fatal(err)
	}
	if len(completer.requests) == 0 {
		t.Fatal("no request was captured")
	}
	injected, ok := findSummaryMessage(completer.requests[0].Messages)
	if !ok {
		t.Fatal("request did not carry the context-summary message")
	}
	if !strings.Contains(injected.Content, "context summary") {
		t.Fatalf("summary content missing header: %q", injected.Content)
	}
	// The injection is user-role framed data: a trailing assistant message is
	// a prefill/continuation hazard on Anthropic-style dialects.
	if injected.Role != provider.RoleUser {
		t.Fatalf("summary role = %q, want user", injected.Role)
	}
	if !strings.Contains(injected.Content, "host-injected") || !strings.Contains(injected.Content, "not a new request") {
		t.Fatalf("summary header lacks host-injected data framing: %q", injected.Content)
	}
	// The injected message is APPENDED after the latest user objective, so the
	// structural prefix keeps its indices (prompt-cache stability).
	messages := completer.requests[0].Messages
	if len(messages) < 2 {
		t.Fatal("request too short to place the summary after the objective")
	}
	if messages[len(messages)-1].Name != SummaryMessageName {
		t.Fatalf("summary message is not the last message: last name=%q role=%q", messages[len(messages)-1].Name, messages[len(messages)-1].Role)
	}
	if len(summ.requests) == 0 {
		t.Fatal("summary provider was never called")
	}
}

// TestSummaryInjectionDoesNotTouchDurableState pins the determinism
// constraint: injection is ephemeral. Two identical runs - one with a
// Summarizer, one without - leave loop.Messages, LastPreparation.Messages, the
// idempotency key, and the committed request bytes identical.
func TestSummaryInjectionDoesNotTouchDurableState(t *testing.T) {
	run := func(withSummary bool) (*Loop, contextstate.CommitRequest) {
		var summarizer *contextmgr.Summarizer
		if withSummary {
			s := summaryInjectSummarizer(t, &capturingSummaryProvider{})
			summarizer = &s
		}
		loop := &Loop{Completer: &capturingRequestCompleter{}, Tools: tools.NewRegistry()}
		opts := summaryProbeOptions(t, summarizer, &compactingPreparationProbe{compacted: true}, 100_000)
		if _, err := loop.Run(context.Background(), "question", opts); err != nil {
			t.Fatal(err)
		}
		// Normalize wall-clock message timestamps so the two runs compare
		// byte-identically; injection must not change ANY durable byte.
		zeroMessageTimestamps(loop.Messages)
		zeroMessageTimestamps(loop.LastPreparation.Messages)
		principal := opts.PreparationInput.Principal
		result := contextmgr.TurnResult{
			Active: loop.Messages, Ordered: loop.Messages,
			TurnID: 1, Outcome: contextmgr.OutcomeComplete,
			// Events start at Token.Revision.Source+1 (the preparation sits at
			// Source 1, so the turn's first event is Sequence 2), mirroring
			// ProjectSource; a non-contiguous event would be rejected by
			// CommitRequest validation.
			SourceEvents: []contextstate.SourceEvent{{
				ID:   contextstate.SourceID{SessionID: principal.SessionID, Sequence: 2},
				Kind: "message", Role: "user", Provenance: "host", RedactionStatus: "metadata", Size: 8,
			}},
		}
		request, err := contextmgr.BuildCommitRequest(context.Background(), loop.LastPreparation, result, principal, loop.LastPreparation.Token.Revision, loop.LastPreparation.Token.Binding)
		if err != nil {
			t.Fatal(err)
		}
		return loop, request
	}
	loopWith, requestWith := run(true)
	loopWithout, requestWithout := run(false)

	if !reflect.DeepEqual(loopWith.Messages, loopWithout.Messages) {
		t.Fatal("summary injection changed loop history")
	}
	if !reflect.DeepEqual(loopWith.LastPreparation.Messages, loopWithout.LastPreparation.Messages) {
		t.Fatal("summary injection changed LastPreparation.Messages")
	}
	if loopWith.LastPreparation.Token.IdempotencyKey != loopWithout.LastPreparation.Token.IdempotencyKey {
		t.Fatal("summary injection changed the idempotency key")
	}
	if requestWith.BaseDigest != requestWithout.BaseDigest || requestWith.Fingerprint != requestWithout.Fingerprint {
		t.Fatalf("summary injection changed commit bytes: digest=%q/%q fingerprint=%q/%q",
			requestWith.BaseDigest, requestWithout.BaseDigest, requestWith.Fingerprint, requestWithout.Fingerprint)
	}
	withBytes, err := contextstate.MarshalCanonical(requestWith)
	if err != nil {
		t.Fatal(err)
	}
	withoutBytes, err := contextstate.MarshalCanonical(requestWithout)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(withBytes, withoutBytes) {
		t.Fatal("summary injection changed the serialized commit request")
	}
}

// zeroMessageTimestamps clears the wall-clock CreatedAt on messages in place
// so two runs produced at different instants compare byte-identically.
func zeroMessageTimestamps(messages []provider.Message) {
	for index := range messages {
		messages[index].CreatedAt = time.Time{}
	}
}

// TestSummaryInjectionIdempotencyKeyStableAcrossRuns drives two identical runs
// through the REAL structural planner (a genuinely compacted preparation) and
// asserts the structural idempotency key is identical despite injection.
func TestSummaryInjectionIdempotencyKeyStableAcrossRuns(t *testing.T) {
	run := func() string {
		provider := &capturingSummaryProvider{}
		summarizer := summaryInjectSummarizer(t, provider)
		principal, err := contextstate.NewPrincipal("workspace", "session", "subject")
		if err != nil {
			t.Fatal(err)
		}
		binding, err := contextstate.NewBindingRevision("summary-test", "model", 1)
		if err != nil {
			t.Fatal(err)
		}
		revision := contextstate.Revision{Session: 1, Durable: 1, Source: 1}
		opts := Options{
			Model: "model", MaxContextTokens: 10_000, MaxSteps: 3,
			PreparationManager: contextmgr.StructuralPreparationManager{},
			PreparationInput: contextmgr.PrepareInput{
				Budget: 10_000, Principal: principal, Binding: binding, Revision: revision,
				SourceRange: contextstate.SourceRange{
					Start: contextstate.SourceID{SessionID: principal.SessionID, Sequence: revision.Source},
					End:   contextstate.SourceID{SessionID: principal.SessionID, Sequence: revision.Source},
				},
			},
			SummaryConfig: SummaryConfig{Summarizer: &summarizer, Redaction: summaryRedaction()},
		}
		loop := &Loop{Completer: &capturingRequestCompleter{}, Tools: tools.NewRegistry()}
		if _, err := loop.Run(context.Background(), strings.Repeat("content ", 4000), opts); err != nil {
			t.Fatal(err)
		}
		if !loop.HasPreparation || !loop.LastPreparation.Compacted {
			t.Fatal("real planner did not compact the oversized objective")
		}
		if len(provider.requests) == 0 {
			t.Fatal("injection did not reach the summary provider")
		}
		return loop.LastPreparation.Token.IdempotencyKey
	}
	first := run()
	second := run()
	if first == "" || first != second {
		t.Fatalf("structural idempotency key unstable across identical injected runs: %q vs %q", first, second)
	}
}

// TestSummaryInjectionSummarizerErrorFallsBackStructural pins that a
// summarizer failure never fails the turn and never injects a message.
func TestSummaryInjectionSummarizerErrorFallsBackStructural(t *testing.T) {
	provider := &capturingSummaryProvider{err: errors.New("provider down")}
	summarizer := summaryInjectSummarizer(t, provider)
	completer := &capturingRequestCompleter{}
	loop := &Loop{Completer: completer, Tools: tools.NewRegistry()}
	if _, err := loop.Run(context.Background(), "question", summaryProbeOptions(t, &summarizer, &compactingPreparationProbe{compacted: true}, 100_000)); err != nil {
		t.Fatalf("summarizer error failed the turn: %v", err)
	}
	if anyRequestCarriesSummary(completer.requests) {
		t.Fatal("summarizer error still injected a summary")
	}
}

// TestSummaryInjectionRedactionRefusalFallsBack pins that a redaction-classified
// envelope falls back structural-only (INV-SEC-4 summaries-refuse behavior).
func TestSummaryInjectionRedactionRefusalFallsBack(t *testing.T) {
	provider := &capturingSummaryProvider{}
	summarizer := summaryInjectSummarizer(t, provider)
	opts := summaryProbeOptions(t, &summarizer, &compactingPreparationProbe{compacted: true}, 100_000)
	opts.SummaryConfig.Redaction = contextstate.RedactionPolicy{
		Configured: true,
		Classifier: func(data []byte) error {
			if strings.Contains(string(data), "question") {
				return contextstate.ErrInvalidDTO
			}
			return nil
		},
	}
	completer := &capturingRequestCompleter{}
	loop := &Loop{Completer: completer, Tools: tools.NewRegistry()}
	if _, err := loop.Run(context.Background(), "question", opts); err != nil {
		t.Fatalf("redaction refusal failed the turn: %v", err)
	}
	if anyRequestCarriesSummary(completer.requests) {
		t.Fatal("redaction-classified summary was injected")
	}
}

// TestSummaryInjectionOverBudgetFallsBack pins that an injected message that
// would push the retained request past the context budget is dropped.
func TestSummaryInjectionOverBudgetFallsBack(t *testing.T) {
	provider := &capturingSummaryProvider{}
	summarizer := summaryInjectSummarizer(t, provider)
	completer := &capturingRequestCompleter{}
	loop := &Loop{Completer: completer, Tools: tools.NewRegistry()}
	// AfterTokens=400 from the probe; budget=400 means any injected message
	// pushes past the budget, so the request falls back structural-only.
	if _, err := loop.Run(context.Background(), "question", summaryProbeOptions(t, &summarizer, &compactingPreparationProbe{compacted: true}, 400)); err != nil {
		t.Fatalf("over-budget injection failed the turn: %v", err)
	}
	if anyRequestCarriesSummary(completer.requests) {
		t.Fatal("over-budget summary was injected")
	}
}

// TestSummaryInjectionNonCompactedNeverInjects pins that a non-compacted
// preparation never triggers a summary provider call or an injected message.
func TestSummaryInjectionNonCompactedNeverInjects(t *testing.T) {
	provider := &capturingSummaryProvider{}
	summarizer := summaryInjectSummarizer(t, provider)
	completer := &capturingRequestCompleter{}
	loop := &Loop{Completer: completer, Tools: tools.NewRegistry()}
	if _, err := loop.Run(context.Background(), "question", summaryProbeOptions(t, &summarizer, &compactingPreparationProbe{}, 100_000)); err != nil {
		t.Fatal(err)
	}
	if anyRequestCarriesSummary(completer.requests) {
		t.Fatal("non-compacted preparation injected a summary")
	}
	if len(provider.requests) != 0 {
		t.Fatal("non-compacted preparation called the summary provider")
	}
}

// TestSummaryInjectionTurnStateFactsReachProvider drives the white-box
// injection path with a pre-populated tracker: evidence, changed surfaces,
// risks, and state all land in the envelope the provider receives, and the
// rendered summary is placed in the ephemeral request without touching loop
// history.
func TestSummaryInjectionTurnStateFactsReachProvider(t *testing.T) {
	provider := &capturingSummaryProvider{}
	summarizer := summaryInjectSummarizer(t, provider)
	facts := contextmgr.NewTurnState()
	if err := facts.AddEvidence("user message (~1 KiB)"); err != nil {
		t.Fatal(err)
	}
	if err := facts.AddChangedSurface("write_file"); err != nil {
		t.Fatal(err)
	}
	if err := facts.AddRisk("tool read_file failed: boom"); err != nil {
		t.Fatal(err)
	}
	if err := facts.SetState("latest assistant state"); err != nil {
		t.Fatal(err)
	}
	loop := summaryInjectedLoopFixture(t, facts)
	opts := Options{
		MaxContextTokens: 100_000,
		SummaryConfig:    SummaryConfig{Summarizer: &summarizer, Redaction: summaryRedaction()},
	}
	messages := loop.injectSummary(context.Background(), opts)
	if len(provider.requests) != 1 {
		t.Fatalf("provider calls=%d, want 1", len(provider.requests))
	}
	got := provider.requests[0].Input
	if !slices.Contains(got.Evidence, "user message (~1 KiB)") {
		t.Fatalf("evidence missing: %v", got.Evidence)
	}
	if !slices.Contains(got.ChangedSurfaces, "write_file") {
		t.Fatalf("changed surfaces missing: %v", got.ChangedSurfaces)
	}
	if !slices.Contains(got.Risks, "tool read_file failed: boom") {
		t.Fatalf("risks missing: %v", got.Risks)
	}
	if got.State != "latest assistant state" {
		t.Fatalf("state = %q", got.State)
	}
	if got.Objective != "question" {
		t.Fatalf("objective = %q", got.Objective)
	}
	injected, ok := findSummaryMessage(messages)
	if !ok {
		t.Fatal("injected request has no summary message")
	}
	if !strings.Contains(injected.Content, "context summary") {
		t.Fatalf("summary content missing header: %q", injected.Content)
	}
	if len(loop.Messages) != 1 || loop.Messages[0].Name == SummaryMessageName {
		t.Fatal("injection mutated loop history")
	}
}

// summaryInjectedLoopFixture builds a Loop that already holds a compacted
// preparation and a populated tracker, ready for injectSummary.
func summaryInjectedLoopFixture(t *testing.T, facts *contextmgr.TurnState) *Loop {
	t.Helper()
	principal, err := contextstate.NewPrincipal("workspace", "session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := contextstate.NewBindingRevision("summary-test", "model", 1)
	if err != nil {
		t.Fatal(err)
	}
	revision := contextstate.Revision{Session: 1, Durable: 1, Source: 1}
	rangeValue := contextstate.SourceRange{
		Start: contextstate.SourceID{SessionID: principal.SessionID, Sequence: revision.Source},
		End:   contextstate.SourceID{SessionID: principal.SessionID, Sequence: revision.Source},
	}
	prep, err := contextmgr.CapturePreparation(
		contextmgr.PrepareInput{Messages: []provider.Message{{Role: provider.RoleUser, Content: "question"}}, Budget: 100_000, Principal: principal, Binding: binding, Revision: revision},
		contextmgr.CheckpointCandidate{SourceRange: rangeValue, ActiveContext: []byte("active")},
		[]provider.Message{{Role: provider.RoleUser, Content: "question"}}, true, "summary-facts-test",
	)
	if err != nil {
		t.Fatal(err)
	}
	prep.AfterTokens = 100
	loop := &Loop{Completer: preparationSuccessCompleter{}, Tools: tools.NewRegistry(), TurnState: facts}
	loop.HasPreparation = true
	loop.LastPreparation = prep
	loop.Messages = []provider.Message{{Role: provider.RoleUser, Content: "question"}}
	return loop
}

// TestInjectSummaryMessageAppendsAfterPrefix pins the cache-friendly shape:
// every pre-existing message keeps its exact index (the provider prompt-cache
// prefix stays valid) and the summary is appended last.
func TestInjectSummaryMessageAppendsAfterPrefix(t *testing.T) {
	structural := []provider.Message{
		{Role: provider.RoleSystem, Content: "system"},
		{Role: provider.RoleUser, Content: "old question"},
		{Role: provider.RoleAssistant, Content: "old answer"},
		{Role: provider.RoleUser, Content: "objective"},
	}
	injected := provider.Message{Role: provider.RoleUser, Content: "summary", Name: SummaryMessageName}
	out := InjectSummaryMessage(structural, injected)
	if len(out) != len(structural)+1 {
		t.Fatalf("len=%d, want %d", len(out), len(structural)+1)
	}
	for index := range structural {
		if !reflect.DeepEqual(out[index], structural[index]) {
			t.Fatalf("structural message at index %d changed: %+v", index, out[index])
		}
	}
	if out[len(out)-1].Name != SummaryMessageName {
		t.Fatalf("summary is not last: %+v", out[len(out)-1])
	}
	// Empty history: the summary is the only message.
	solo := InjectSummaryMessage(nil, injected)
	if len(solo) != 1 || solo[0].Name != SummaryMessageName {
		t.Fatalf("empty-history inject = %+v", solo)
	}
}

// TestStepRequestOrderSummaryThenConcludeNudge pins the ephemeral request
// order when both injections fire: structural prefix unchanged, then the
// summary, then the soft-conclude nudge LAST.
func TestStepRequestOrderSummaryThenConcludeNudge(t *testing.T) {
	summ := &capturingSummaryProvider{}
	summarizer := summaryInjectSummarizer(t, summ)
	loop := summaryInjectedLoopFixture(t, contextmgr.NewTurnState())
	// MaxToolCalls=1 leaves fewer than concludeToolCallsLeft reservations, so
	// the conclude nudge fires on the next request.
	loop.workLimits = &workLimitMeter{limits: runtime.WorkLimits{MaxToolCalls: 1}}
	structural := append([]provider.Message(nil), loop.Messages...)
	opts := Options{
		MaxContextTokens: 100_000,
		SummaryConfig:    SummaryConfig{Summarizer: &summarizer, Redaction: summaryRedaction()},
	}
	req, err := loop.stepRequest(context.Background(), nil, opts, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	messages := req.Messages
	if len(messages) != len(structural)+2 {
		t.Fatalf("len=%d, want structural+summary+nudge=%d", len(messages), len(structural)+2)
	}
	for index := range structural {
		if !reflect.DeepEqual(messages[index], structural[index]) {
			t.Fatalf("structural message at index %d changed: %+v", index, messages[index])
		}
	}
	if messages[len(structural)].Name != SummaryMessageName {
		t.Fatalf("summary is not directly after the structural prefix: %+v", messages[len(structural)])
	}
	last := messages[len(messages)-1]
	if last.Role != provider.RoleUser || last.Content != concludeMessage {
		t.Fatalf("conclude nudge is not last: %+v", last)
	}
	if !reflect.DeepEqual(loop.Messages, structural) {
		t.Fatal("stepRequest mutated loop history")
	}
}

// stepKeyedCompactingProbe compacts on the listed Prepare calls (1-based),
// each compaction under a DISTINCT idempotency key. Each compaction is
// therefore a separate event for the summary memo, unlike
// compactingPreparationProbe whose fixed key models ONE compaction event
// re-prepared each step.
type stepKeyedCompactingProbe struct {
	compactOn map[int]bool
	calls     int
}

func (p *stepKeyedCompactingProbe) Prepare(_ context.Context, input contextmgr.PrepareInput) (contextmgr.Preparation, error) {
	p.calls++
	rangeValue := contextstate.SourceRange{
		Start: contextstate.SourceID{SessionID: input.Principal.SessionID, Sequence: input.Revision.Source},
		End:   contextstate.SourceID{SessionID: input.Principal.SessionID, Sequence: input.Revision.Source},
	}
	prep, err := contextmgr.CapturePreparation(input, contextmgr.CheckpointCandidate{
		SourceRange: rangeValue, ActiveContext: []byte("active"),
	}, input.Messages, p.compactOn[p.calls], fmt.Sprintf("summary-keyed-%d", p.calls))
	if err != nil {
		return contextmgr.Preparation{}, err
	}
	prep.BeforeTokens = 1000
	prep.AfterTokens = 400
	return prep, nil
}

func (p *stepKeyedCompactingProbe) Discard(contextmgr.Preparation) {}

// TestSummaryInjectionToolFactsReachLaterRequest drives the recording seam
// through a real tool call: the write_file execution on step 1 lands in the
// evidence and changed surfaces of the step-2 request's envelope. Step 2
// compacts AGAIN under a new key, so its summary is a fresh Summarize that
// sees the accumulated facts (a repeat of the SAME compaction event would be
// served from the memo instead).
func TestSummaryInjectionToolFactsReachLaterRequest(t *testing.T) {
	provider := &capturingSummaryProvider{}
	summarizer := summaryInjectSummarizer(t, provider)
	completer := &capturingRequestCompleter{toolStep: true}
	reg := tools.NewRegistry()
	reg.Register(&writeFakeTool{name: "write_file"})
	loop := &Loop{Completer: completer, Tools: reg}
	probe := &stepKeyedCompactingProbe{compactOn: map[int]bool{1: true, 2: true}}
	if _, err := loop.Run(context.Background(), "question", summaryProbeOptions(t, &summarizer, probe, 100_000)); err != nil {
		t.Fatal(err)
	}
	if len(completer.requests) < 2 {
		t.Fatalf("requests=%d, want two steps", len(completer.requests))
	}
	if len(provider.requests) < 2 {
		t.Fatalf("provider requests=%d, want two", len(provider.requests))
	}
	last := provider.requests[len(provider.requests)-1].Input
	if !slices.Contains(last.Evidence, "write_file") {
		t.Fatalf("tool name missing from evidence: %v", last.Evidence)
	}
	if !slices.Contains(last.ChangedSurfaces, "write_file") {
		t.Fatalf("write-class tool missing from changed surfaces: %v", last.ChangedSurfaces)
	}
}

// TestSummaryInjectionRetryPath drives the prompt-too-long compact-and-retry
// path with a Summarizer wired (R0-2): a large history forces the provider
// rejection, the retried request carries the context-summary message, and the
// retry's envelope references the messages the retried (pruned) history
// actually dropped. The first prepareStep captured evidence for the rejected,
// never-sent history; the fix re-derives the omitted diff from the pre-prune
// vs pruned history before the retry request is built (R0-1).
func TestSummaryInjectionRetryPath(t *testing.T) {
	summ := &capturingSummaryProvider{}
	summarizer := summaryInjectSummarizer(t, summ)
	comp := &promptTooLongCompleter{
		failN:            1,
		promptTooLongErr: fmt.Errorf("%w", provider.ErrPromptTooLong),
		steps:            []provider.Response{{Content: "recovered", FinishReason: "stop"}},
	}
	loop := &Loop{Completer: comp, Tools: tools.NewRegistry(), Messages: buildOversizedHistory()}
	text, err := loop.Run(context.Background(), "final question", summaryProbeOptions(t, &summarizer, &compactingPreparationProbe{compacted: true}, 100_000))
	if err != nil {
		t.Fatalf("run failed after one compaction retry: %v", err)
	}
	if text != "recovered" {
		t.Fatalf("text = %q, want %q", text, "recovered")
	}
	if comp.calls != 2 {
		t.Fatalf("completer called %d times, want 2 (fail + retry)", comp.calls)
	}
	// The retry request carries the summary.
	injected, ok := findSummaryMessage(comp.lastReq.Messages)
	if !ok {
		t.Fatal("retry request did not carry the context-summary message")
	}
	// The retry envelope re-derived the omitted evidence for the pruned
	// history: the first (rejected) attempt had no planner elision (the probe
	// retains every message it sees), while the retry's envelope references
	// the oldest turn the prompt-too-long prune dropped from the history.
	if len(summ.requests) != 2 {
		t.Fatalf("summary provider saw %d requests, want 2 (first attempt + retry)", len(summ.requests))
	}
	if len(summ.requests[0].Input.Evidence) != 0 {
		t.Fatalf("first attempt envelope has evidence %v, want none (no planner elision with the probe)", summ.requests[0].Input.Evidence)
	}
	retryEvidence := summ.requests[1].Input.Evidence
	if !slices.Contains(retryEvidence, "user message (~16 KiB)") || !slices.Contains(retryEvidence, "assistant message (~16 KiB)") {
		t.Fatalf("retry envelope does not reference the pruned turn: %v", retryEvidence)
	}
	for _, item := range retryEvidence {
		if strings.Contains(item, "turn") {
			t.Fatalf("retry evidence leaks message content: %q", item)
		}
	}
	// The retried request's summary message renders the re-derived evidence.
	if !strings.Contains(injected.Content, "evidence: user message (~16 KiB); assistant message (~16 KiB)") {
		t.Fatalf("retry summary message does not render the re-derived evidence: %q", injected.Content)
	}
}

// droppingPreparationProbe retains only the tail of the input history, so the
// preparation really drops the head messages.
type droppingPreparationProbe struct {
	compactingPreparationProbe
	drop int
}

func (p *droppingPreparationProbe) Prepare(ctx context.Context, input contextmgr.PrepareInput) (contextmgr.Preparation, error) {
	rangeValue := contextstate.SourceRange{
		Start: contextstate.SourceID{SessionID: input.Principal.SessionID, Sequence: input.Revision.Source},
		End:   contextstate.SourceID{SessionID: input.Principal.SessionID, Sequence: input.Revision.Source},
	}
	retained := input.Messages
	if p.drop <= len(retained) {
		retained = retained[p.drop:]
	}
	prep, err := contextmgr.CapturePreparation(input, contextmgr.CheckpointCandidate{
		SourceRange: rangeValue, ActiveContext: []byte("active"),
	}, retained, true, "summary-excerpt-test")
	if err != nil {
		return contextmgr.Preparation{}, err
	}
	prep.BeforeTokens = 1000
	prep.AfterTokens = 400
	return prep, nil
}

// TestSummaryInjectionCarriesDroppedContent pins the loop path: a preparation
// that drops the head of the history must put the dropped messages' real
// content on the summary request, not just size labels.
func TestSummaryInjectionCarriesDroppedContent(t *testing.T) {
	summ := &capturingSummaryProvider{}
	summarizer := summaryInjectSummarizer(t, summ)
	completer := &capturingRequestCompleter{}
	loop := &Loop{Completer: completer, Tools: tools.NewRegistry()}
	loop.Messages = append(loop.Messages,
		provider.Message{Role: provider.RoleUser, Content: "earlier task about the auth module"},
		provider.Message{Role: provider.RoleAssistant, Content: "I moved jwt.go into internal/auth and added rotation."},
	)
	if _, err := loop.Run(context.Background(), "question", summaryProbeOptions(t, &summarizer, &droppingPreparationProbe{drop: 2}, 100_000)); err != nil {
		t.Fatal(err)
	}
	if len(summ.requests) == 0 {
		t.Fatal("summary provider was never called")
	}
	var texts []string
	for _, excerpt := range summ.requests[0].SourceExcerpts {
		texts = append(texts, excerpt.Text)
	}
	if !slices.ContainsFunc(texts, func(s string) bool { return strings.Contains(s, "jwt.go") }) {
		t.Fatalf("source excerpts carry no dropped content: %v", texts)
	}
	if !slices.ContainsFunc(texts, func(s string) bool { return strings.Contains(s, "auth module") }) {
		t.Fatalf("first user message missing from excerpts: %v", texts)
	}
}
