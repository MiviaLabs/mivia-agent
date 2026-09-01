package chat

import (
	"errors"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

func TestPlainCommitPersistenceErrorWrapsBothCauseAndErrPersistence(t *testing.T) {
	if got := plainCommitPersistenceError(nil); got != nil {
		t.Fatalf("plainCommitPersistenceError(nil) = %v, want nil", got)
	}

	cause := errors.New("disk full")
	got := plainCommitPersistenceError(cause)
	if got == nil {
		t.Fatal("plainCommitPersistenceError(cause) = nil, want a wrapped error")
	}
	if !errors.Is(got, cause) {
		t.Fatalf("error = %v, want errors.Is(err, cause) to hold", got)
	}
	if !errors.Is(got, ErrPersistence) {
		t.Fatalf("error = %v, want errors.Is(err, ErrPersistence) to hold", got)
	}
}

// adoptUncommittedPlainTurnFixture wires a minimal session with a
// self-consistent turn-0 fence, matching how commitPlainContextTurn and its
// siblings capture snapshot.token via captureTurnToken.
func adoptUncommittedPlainTurnFixture(t *testing.T) (*Session, plainTurnSnapshot) {
	t.Helper()
	sess := NewSession(&config.Resolved{ProviderName: "fake", Model: "model", SystemPrompt: "sys"}, nil)
	sess.Messages = []provider.Message{{Role: provider.RoleUser, Content: "original"}}
	snapshot := plainTurnSnapshot{myTurn: sess.turnID, token: sess.captureTurnToken(sess.turnID)}
	return sess, snapshot
}

func TestAdoptUncommittedPlainTurnNoopsOnStaleFence(t *testing.T) {
	sess, snapshot := adoptUncommittedPlainTurnFixture(t)
	before := sess.MessagesCopy()

	// Simulate a newer turn (e.g. a concurrent /clear or a superseding
	// SendUser) starting after snapshot was captured: bump turnID so the
	// snapshot's fence is now stale.
	sess.mu.Lock()
	sess.turnID++
	sess.mu.Unlock()

	candidate := []provider.Message{{Role: provider.RoleUser, Content: "should not land"}}
	sess.adoptUncommittedPlainTurn(candidate, snapshot)

	if got := sess.MessagesCopy(); !messagesEqual(got, before) {
		t.Fatalf("Messages = %+v, want unchanged %+v - a stale fence must not adopt", got, before)
	}
}

func TestAdoptUncommittedPlainTurnNoopsOnInvalidShape(t *testing.T) {
	sess, snapshot := adoptUncommittedPlainTurnFixture(t)
	before := sess.MessagesCopy()

	// An assistant tool-call with no paired tool result is a hard shape
	// error under provider.ValidateToolPairing (via validateRestoredMessages).
	invalid := []provider.Message{
		{Role: provider.RoleUser, Content: "question"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "call-1", Type: "function"}}},
	}
	sess.adoptUncommittedPlainTurn(invalid, snapshot)

	if got := sess.MessagesCopy(); !messagesEqual(got, before) {
		t.Fatalf("Messages = %+v, want unchanged %+v - an unpairable candidate must not adopt", got, before)
	}
}

func TestAdoptUncommittedPlainTurnAdoptsValidCandidateWithoutAdvancingHead(t *testing.T) {
	sess, snapshot := adoptUncommittedPlainTurnFixture(t)
	beforeHead := sess.contextHead
	beforeRevision := sess.contextRevision

	candidate := []provider.Message{
		{Role: provider.RoleUser, Content: "question"},
		{Role: provider.RoleAssistant, Content: "answer"},
	}
	sess.adoptUncommittedPlainTurn(candidate, snapshot)

	if got := sess.MessagesCopy(); !messagesEqual(got, candidate) {
		t.Fatalf("Messages = %+v, want the adopted candidate %+v", got, candidate)
	}
	if got := sess.contextHead; got != beforeHead {
		t.Fatalf("contextHead = %+v, want unchanged %+v - nothing landed durably", got, beforeHead)
	}
	if got := sess.contextRevision; got != beforeRevision {
		t.Fatalf("contextRevision = %+v, want unchanged %+v - nothing landed durably", got, beforeRevision)
	}
}

func messagesEqual(a, b []provider.Message) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Role != b[i].Role || a[i].Content != b[i].Content {
			return false
		}
	}
	return true
}
