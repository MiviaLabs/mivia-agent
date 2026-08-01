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
