package contextmgr

import (
	"context"
	"errors"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

type publicationStore struct {
	commits int
	request contextstate.CommitRequest
	err     error
}

func (s *publicationStore) EnsureSession(context.Context, contextstate.EnsureSessionRequest) error {
	return nil
}

func (s *publicationStore) Commit(_ context.Context, request contextstate.CommitRequest) error {
	s.commits++
	s.request = request
	return s.err
}

func (s *publicationStore) Advance(context.Context, contextstate.AdvanceRequest) error { return nil }

func (s *publicationStore) Load(context.Context, contextstate.Principal, string) (contextstate.Snapshot, error) {
	return contextstate.Snapshot{}, nil
}

func TestFailedCommitLeavesStateUnchanged(t *testing.T) {
	cases := []struct {
		name       string
		provider   SummaryProvider
		storeError error
		wantStore  int
	}{
		{
			name: "provider failure",
			provider: summaryProviderFunc(func(context.Context, SummaryRequest) (Summary, error) {
				return Summary{}, errors.New("provider unavailable")
			}),
		},
		{
			name: "summary validation failure",
			provider: summaryProviderFunc(func(_ context.Context, request SummaryRequest) (Summary, error) {
				return Summary{Version: 99, SourceRange: request.SourceRange}, nil
			}),
		},
		{
			name:     "persistence failure",
			provider: validSummaryProvider(), storeError: errors.New("persistence failed"), wantStore: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, summarizer, summaryRequest, preparation, result := publicationFixture(t, tc.provider)
			store.err = tc.storeError
			before := preparation
			err := CommitPreparation(context.Background(), PublicationRequest{
				Store: store, Summarizer: &summarizer, SummaryRequest: summaryRequest,
				Preparation: preparation, Result: result,
			})
			if err == nil {
				t.Fatal("failed publication returned nil")
			}
			if store.commits != tc.wantStore {
				t.Fatalf("store commits=%d, want %d", store.commits, tc.wantStore)
			}
			if preparation.Candidate.SummaryMetadata != nil || before.Candidate.SummaryMetadata != nil {
				t.Fatal("caller preparation was mutated by failed publication")
			}
		})
	}
}

func TestCommitPreparationPublishesOnlyAfterValidatedSummary(t *testing.T) {
	store, summarizer, summaryRequest, preparation, result := publicationFixture(t, validSummaryProvider())
	if err := CommitPreparation(context.Background(), PublicationRequest{
		Store: store, Summarizer: &summarizer, SummaryRequest: summaryRequest,
		Preparation: preparation, Result: result,
	}); err != nil {
		t.Fatal(err)
	}
	if store.commits != 1 || len(store.request.Checkpoint.SummaryMetadata) == 0 {
		t.Fatalf("publication request = %+v", store.request)
	}
}

func TestCommitPreparationAllowsStructuralCompactionWithoutSummary(t *testing.T) {
	store, _, _, preparation, result := publicationFixture(t, validSummaryProvider())
	if err := CommitPreparation(context.Background(), PublicationRequest{
		Store: store, Preparation: preparation, Result: result,
	}); err != nil {
		t.Fatalf("structural compaction commit: %v", err)
	}
}

// summaryRequestBuilderForPrep derives a summary request from the preparation
// itself: the objective from the latest retained user message and the source
// range from preparation.Token.Range. Everything else is copied from a fixed
// template request.
func summaryRequestBuilderForPrep(template SummaryRequest) SummaryRequestBuilder {
	return func(preparation Preparation) (SummaryRequest, error) {
		objective := ""
		for index := len(preparation.Messages) - 1; index >= 0; index-- {
			if preparation.Messages[index].Role == provider.RoleUser {
				objective = preparation.Messages[index].Content
				break
			}
		}
		envelope, err := NewSummaryEnvelope(template.Input.Version, objective, template.Input.State, nil, nil, nil, nil, nil, preparation.Token.Range, template.Input.PolicyDigest)
		if err != nil {
			return SummaryRequest{}, err
		}
		request := template
		request.Input = envelope
		request.SourceRange = preparation.Token.Range
		if err := request.Validate(); err != nil {
			return SummaryRequest{}, err
		}
		return request, nil
	}
}

// TestCommitPreparationBuilderPersistsSummaryMetadata drives the commit-time
// builder seam: with a builder wired, CommitPreparation derives the request
// from the preparation and persists validated SummaryMetadata on the
// checkpoint when the preparation is compacted.
func TestCommitPreparationBuilderPersistsSummaryMetadata(t *testing.T) {
	store, summarizer, summaryRequest, preparation, result := publicationFixture(t, validSummaryProvider())
	if err := CommitPreparation(context.Background(), PublicationRequest{
		Store: store, Summarizer: &summarizer, SummaryBuilder: summaryRequestBuilderForPrep(summaryRequest),
		Preparation: preparation, Result: result,
	}); err != nil {
		t.Fatal(err)
	}
	if store.commits != 1 || len(store.request.Checkpoint.SummaryMetadata) == 0 {
		t.Fatalf("builder publication = %+v", store.request)
	}
}

// TestCommitPreparationBuilderErrorLeavesStateUntouched pins the fail-before-
// CAS contract: a builder failure returns before any Store.Commit call and
// never mutates the caller's preparation.
func TestCommitPreparationBuilderErrorLeavesStateUntouched(t *testing.T) {
	store, summarizer, _, preparation, result := publicationFixture(t, validSummaryProvider())
	before := preparation
	builder := func(Preparation) (SummaryRequest, error) {
		return SummaryRequest{}, errors.New("builder failed")
	}
	if err := CommitPreparation(context.Background(), PublicationRequest{
		Store: store, Summarizer: &summarizer, SummaryBuilder: builder,
		Preparation: preparation, Result: result,
	}); err == nil {
		t.Fatal("builder failure returned nil")
	}
	if store.commits != 0 {
		t.Fatalf("store commits=%d, want 0", store.commits)
	}
	if preparation.Candidate.SummaryMetadata != nil || before.Candidate.SummaryMetadata != nil {
		t.Fatal("caller preparation was mutated by failed builder")
	}
}

// TestCommitPreparationNonCompactedNeverSummarizes pins that a non-compacted
// preparation never consults the builder or the Summarizer, yet still commits.
func TestCommitPreparationNonCompactedNeverSummarizes(t *testing.T) {
	store, original, summaryRequest, compacted, result := publicationFixture(t, validSummaryProvider())
	nonCompacted := compacted
	nonCompacted.Compacted = false
	var builderCalls, providerCalls int
	captured := summaryProviderFunc(func(_ context.Context, request SummaryRequest) (Summary, error) {
		providerCalls++
		return Summary{Version: request.Input.Version, Objective: "obj", State: "state", SourceRange: request.SourceRange}, nil
	})
	summarizer, err := NewSummarizer(captured, original.Binding, original.Policy)
	if err != nil {
		t.Fatal(err)
	}
	builder := func(Preparation) (SummaryRequest, error) {
		builderCalls++
		return summaryRequest, nil
	}
	if err := CommitPreparation(context.Background(), PublicationRequest{
		Store: store, Summarizer: &summarizer, SummaryBuilder: builder,
		Preparation: nonCompacted, Result: result,
	}); err != nil {
		t.Fatal(err)
	}
	if builderCalls != 0 || providerCalls != 0 {
		t.Fatalf("builder=%d provider=%d, want 0/0 for a non-compacted preparation", builderCalls, providerCalls)
	}
	if store.commits != 1 {
		t.Fatalf("store commits=%d, want 1", store.commits)
	}
}

// TestCommitPreparationBuilderUsesPreparationState pins that the builder's
// derived request reads the objective from the latest retained user message
// and the source range from preparation.Token.Range.
func TestCommitPreparationBuilderUsesPreparationState(t *testing.T) {
	store, original, summaryRequest, preparation, result := publicationFixture(t, validSummaryProvider())
	var received SummaryRequest
	captured := summaryProviderFunc(func(_ context.Context, request SummaryRequest) (Summary, error) {
		received = request
		return Summary{Version: request.Input.Version, Objective: request.Input.Objective, State: "state", SourceRange: request.SourceRange}, nil
	})
	summarizer, err := NewSummarizer(captured, original.Binding, original.Policy)
	if err != nil {
		t.Fatal(err)
	}
	if err := CommitPreparation(context.Background(), PublicationRequest{
		Store: store, Summarizer: &summarizer, SummaryBuilder: summaryRequestBuilderForPrep(summaryRequest),
		Preparation: preparation, Result: result,
	}); err != nil {
		t.Fatal(err)
	}
	if received.Input.Objective != "old" {
		t.Fatalf("objective = %q, want the latest retained user message", received.Input.Objective)
	}
	if received.SourceRange != preparation.Token.Range || received.Input.SourceRange != preparation.Token.Range {
		t.Fatalf("source range = %+v / %+v, want preparation.Token.Range %+v", received.SourceRange, received.Input.SourceRange, preparation.Token.Range)
	}
}

func validSummaryProvider() SummaryProvider {
	return summaryProviderFunc(func(_ context.Context, request SummaryRequest) (Summary, error) {
		return Summary{Version: request.Input.Version, Objective: "safe objective", State: "safe state", SourceRange: request.SourceRange}, nil
	})
}

func publicationFixture(t *testing.T, summaryProvider SummaryProvider) (*publicationStore, Summarizer, SummaryRequest, Preparation, TurnResult) {
	t.Helper()
	principal, err := contextstate.NewPrincipal("workspace", "summary-session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := contextstate.NewBindingRevision("provider", "model", 1)
	if err != nil {
		t.Fatal(err)
	}
	source := contextstate.SourceID{SessionID: principal.SessionID, Sequence: 1}
	rangeValue, err := contextstate.NewSourceRange(source, source)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := NewSummaryEnvelope(1, "safe objective", "state", nil, nil, nil, nil, nil, rangeValue, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	redaction := contextstate.RedactionPolicy{Configured: true, Patterns: []string{"never-match"}}
	summaryRequest := SummaryRequest{
		Input: envelope, Budget: 100, OutputLimit: 128, SourceRange: rangeValue,
		Provider: "provider", Model: "model", EndpointAllowlist: []string{"https://summary.invalid"}, RedactionPolicy: redaction,
	}
	policy := contextstate.PolicySnapshot{
		SummaryEnabled: true, RedactionConfigured: true, Provider: "provider", Model: "model",
		CredentialScope: "scope", NetworkEnabled: true, EndpointAllowlist: summaryRequest.EndpointAllowlist,
		PolicyDigest: envelope.PolicyDigest,
	}
	summarizer, err := NewSummarizer(summaryProvider, binding, policy)
	if err != nil {
		t.Fatal(err)
	}
	preparation, err := CapturePreparation(
		PrepareInput{Messages: []provider.Message{{Role: provider.RoleUser, Content: "old"}}, Budget: 100, Principal: principal, Binding: binding},
		CheckpointCandidate{ActiveContext: []byte(`{"messages":[]}`), SourceRange: rangeValue},
		[]provider.Message{{Role: provider.RoleUser, Content: "old"}}, true, "publication-1",
	)
	if err != nil {
		t.Fatal(err)
	}
	return &publicationStore{}, summarizer, summaryRequest, preparation, TurnResult{
		Ordered:      []provider.Message{{Role: provider.RoleUser, Content: "question"}, {Role: provider.RoleAssistant, Content: "answer"}},
		Active:       []provider.Message{{Role: provider.RoleSystem, Content: "system"}, {Role: provider.RoleUser, Content: "question"}, {Role: provider.RoleAssistant, Content: "answer"}},
		SourceEvents: []contextstate.SourceEvent{{ID: source, Kind: "message", Role: "user", Provenance: "host", RedactionStatus: "metadata", Size: 1}},
		TurnID:       1, Outcome: OutcomeComplete,
	}
}
