package chat

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// erroringPlainCompleter streams partial (if any) then fails with err - the
// non-interrupt, non-budget provider error a --no-tools chat turn can hit.
type erroringPlainCompleter struct {
	partial string
	err     error
}

func (erroringPlainCompleter) Name() string { return "erroring-plain" }
func (c erroringPlainCompleter) Chat(context.Context, provider.Request) (string, error) {
	return "", c.err
}
func (c erroringPlainCompleter) ChatStream(_ context.Context, _ provider.Request, w io.Writer) (string, error) {
	if w != nil && c.partial != "" {
		_, _ = io.WriteString(w, c.partial)
	}
	return "", c.err
}
func (c erroringPlainCompleter) ChatTurn(_ context.Context, req provider.Request) (*provider.Response, error) {
	if req.StreamWriter != nil && c.partial != "" {
		_, _ = io.WriteString(req.StreamWriter, c.partial)
	}
	return nil, c.err
}

// TestNoMessageLossErroredPlainContextTurnIsPersisted is the durable-context
// counterpart: sendPlainContext previously kept "discard-and-drop behavior"
// for any non-interrupted error (per its own doc comment). The turn must now
// commit durably (tagged contextmgr.OutcomeUpstreamErr) while the original
// error still surfaces unchanged.
func TestNoMessageLossErroredPlainContextTurnIsPersisted(t *testing.T) {
	upstream := errors.New("upstream 500")
	const partial = "Both fixes work. Here is the pro"
	const question = "prove it"
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sess := NewSession(&config.Resolved{ProviderName: "fake", Model: "model", SystemPrompt: "sys"}, erroringPlainCompleter{partial: partial, err: upstream})
	principal, err := contextstate.NewPrincipal("workspace", sess.SessionID, "subject")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := contextstate.NewBindingRevision("fake", "model", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSession(context.Background(), contextstate.EnsureSessionRequest{Principal: principal, Binding: binding}); err != nil {
		t.Fatal(err)
	}
	manager := &contextmgr.ContextManager{
		PreparationManager:  contextmgr.StructuralPreparationManager{},
		CheckpointPublisher: contextmgr.PreparationCommitter{Store: store},
		Enabled:             true,
	}
	if err := sess.SetContextManager(manager, principal); err != nil {
		t.Fatal(err)
	}
	sess.SetContextStore(store)

	var sink strings.Builder
	_, sendErr := sess.SendUser(context.Background(), question, &sink)
	if !errors.Is(sendErr, upstream) {
		t.Fatalf("SendUser error = %v, want the original upstream error surfaced unchanged", sendErr)
	}

	assertInterruptedPlainPersisted(t, sess.MessagesCopy(), partial, question)

	snapshot, err := store.Load(context.Background(), principal, principal.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	var loaded []provider.Message
	if err := contextstate.UnmarshalCanonical(snapshot.Active.ActiveContext, &loaded); err != nil {
		t.Fatal(err)
	}
	assertInterruptedPlainPersisted(t, loaded, partial, question)
}

// TestErroredPlainContextTurnCommitFailureKeepsTheTurnUnwrapped covers the
// case TestNoMessageLossErroredPlainContextTurnIsPersisted does not: the
// durable commit ITSELF also fails (not just the upstream provider call). On
// this path (commitErroredPlainContext), the exchange must still be adopted
// into memory (best-effort catch-up on the next successful commit), and the
// original upstream error must surface UNCHANGED - deliberately NOT tagged
// with ErrPersistence, unlike commitPlainContextTurn's and
// commitInterruptedPlainContext's own commit-failure branches. The stream
// itself failed upstream here, so the buffered partial text is incomplete
// and untrustworthy; tagging it ErrPersistence would wrongly claim "the
// answer is fine, only the save failed."
func TestErroredPlainContextTurnCommitFailureKeepsTheTurnUnwrapped(t *testing.T) {
	upstream := errors.New("upstream 500")
	commitFailure := errors.New("disk full")
	const partial = "Both fixes work. Here is the pro"
	const question = "prove it"
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sess := NewSession(&config.Resolved{ProviderName: "fake", Model: "model", SystemPrompt: "sys"}, erroringPlainCompleter{partial: partial, err: upstream})
	principal, err := contextstate.NewPrincipal("workspace", sess.SessionID, "subject")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := contextstate.NewBindingRevision("fake", "model", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSession(context.Background(), contextstate.EnsureSessionRequest{Principal: principal, Binding: binding}); err != nil {
		t.Fatal(err)
	}
	manager := &contextmgr.ContextManager{
		PreparationManager:  contextmgr.StructuralPreparationManager{},
		CheckpointPublisher: plainCommitFailurePublisher{err: commitFailure},
		Enabled:             true,
	}
	if err := sess.SetContextManager(manager, principal); err != nil {
		t.Fatal(err)
	}
	sess.SetContextStore(store)
	beforeRevision := sess.contextRevision
	beforeHead := sess.contextHead

	var sink strings.Builder
	_, sendErr := sess.SendUser(context.Background(), question, &sink)
	if !errors.Is(sendErr, upstream) {
		t.Fatalf("SendUser error = %v, want it to wrap the original upstream error", sendErr)
	}
	if errors.Is(sendErr, ErrPersistence) {
		t.Fatalf("SendUser error = %v, want it NOT tagged ErrPersistence - the stream itself failed, not just the save", sendErr)
	}

	assertInterruptedPlainPersisted(t, sess.MessagesCopy(), partial, question)
	if got := sess.contextRevision; got != beforeRevision {
		t.Fatalf("contextRevision = %+v, want unchanged %+v - nothing landed durably", got, beforeRevision)
	}
	if got := sess.contextHead; got != beforeHead {
		t.Fatalf("contextHead = %+v, want unchanged %+v - nothing landed durably", got, beforeHead)
	}
}
