package chat

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

type contextPreparationProbe struct {
	prepares int
	discards int
}

func (p *contextPreparationProbe) Prepare(_ context.Context, input contextmgr.PrepareInput) (contextmgr.Preparation, error) {
	p.prepares++
	rangeValue := contextstate.SourceRange{
		Start: contextstate.SourceID{SessionID: input.Principal.SessionID, Sequence: input.Revision.Source},
		End:   contextstate.SourceID{SessionID: input.Principal.SessionID, Sequence: input.Revision.Source},
	}
	active, err := contextstate.MarshalCanonical(input.Messages)
	if err != nil {
		return contextmgr.Preparation{}, err
	}
	return contextmgr.CapturePreparation(input, contextmgr.CheckpointCandidate{
		ActiveContext: active, SourceRange: rangeValue,
	}, input.Messages, false, "prepare-test")
}

func (p *contextPreparationProbe) Discard(contextmgr.Preparation) { p.discards++ }

type contextPublisherProbe struct {
	commits int
	err     error
}

func (p *contextPublisherProbe) Commit(_ context.Context, _ contextmgr.Preparation, _ contextmgr.TurnResult) error {
	p.commits++
	return p.err
}

func contextSessionManager(t *testing.T, session *Session, publisherErr error) (*contextPreparationProbe, *contextPublisherProbe) {
	t.Helper()
	principal, err := contextstate.NewPrincipal("workspace", session.SessionID, "subject")
	if err != nil {
		t.Fatal(err)
	}
	prep := &contextPreparationProbe{}
	pub := &contextPublisherProbe{err: publisherErr}
	manager := &contextmgr.ContextManager{
		PreparationManager: prep, CheckpointPublisher: pub, Enabled: true,
	}
	if err := session.SetContextManager(manager, principal); err != nil {
		t.Fatal(err)
	}
	return prep, pub
}

func TestPlainTurnUsesPreparationTransaction(t *testing.T) {
	session := NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, &fakeCompleter{out: "answer"})
	prep, pub := contextSessionManager(t, session, nil)
	if _, err := session.SendUser(context.Background(), "question", io.Discard); err != nil {
		t.Fatal(err)
	}
	if prep.prepares != 1 || pub.commits != 1 || prep.discards != 1 {
		t.Fatalf("prepares=%d commits=%d discards=%d", prep.prepares, pub.commits, prep.discards)
	}
	if session.Store() != nil || session.SessionDir != "" {
		t.Fatal("context-enabled turn retained a legacy JSONL store")
	}
}

func TestCheckpointFailureDoesNotFallbackToJSONL(t *testing.T) {
	session := NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, &fakeCompleter{out: "answer"})
	prep, pub := contextSessionManager(t, session, errors.New("checkpoint failed"))
	if _, err := session.SendUser(context.Background(), "question", io.Discard); err == nil {
		t.Fatal("checkpoint failure was swallowed")
	}
	if pub.commits != 1 || prep.discards != 1 || session.MessagesCount() != 0 {
		t.Fatalf("commits=%d discards=%d messages=%d", pub.commits, prep.discards, session.MessagesCount())
	}
	if session.Store() != nil || session.SessionDir != "" {
		t.Fatal("checkpoint failure fell back to legacy JSONL")
	}
}

func TestContextManagerLoadsStoreHeadWhenAttachedSecond(t *testing.T) {
	session := NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, &fakeCompleter{out: "answer"})
	principal, err := contextstate.NewPrincipal("workspace", session.SessionID, "subject")
	if err != nil {
		t.Fatal(err)
	}
	store := &contextHeadProbeStore{revision: contextstate.Revision{Session: 4, Durable: 3, Source: 8}}
	if err := session.SetContextStore(store); err != nil {
		t.Fatal(err)
	}
	if err := session.SetContextManager(&contextmgr.ContextManager{PreparationManager: &contextPreparationProbe{}, CheckpointPublisher: &contextPublisherProbe{}, Enabled: true}, principal); err != nil {
		t.Fatal(err)
	}
	if session.contextHead != store.revision {
		t.Fatalf("context head = %+v, want %+v", session.contextHead, store.revision)
	}
}

type contextHeadProbeStore struct {
	revision contextstate.Revision
	loaded   contextstate.Principal
}

func (s *contextHeadProbeStore) EnsureSession(context.Context, contextstate.EnsureSessionRequest) error {
	return nil
}

func (s *contextHeadProbeStore) Commit(context.Context, contextstate.CommitRequest) error { return nil }

func (s *contextHeadProbeStore) Advance(context.Context, contextstate.AdvanceRequest) error {
	return nil
}

func (s *contextHeadProbeStore) Load(_ context.Context, principal contextstate.Principal, _ string) (contextstate.Snapshot, error) {
	s.loaded = principal
	return contextstate.Snapshot{Revision: s.revision}, nil
}

// Keep the probe tied to the provider-facing contract used by the session.
var _ provider.Completer = (*fakeCompleter)(nil)
var _ contextstate.Store = (*contextHeadProbeStore)(nil)
