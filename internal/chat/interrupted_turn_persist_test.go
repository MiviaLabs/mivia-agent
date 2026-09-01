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

// plainInterruptedCompleter streams a partial answer into the writer, then
// reports the turn was cancelled - exactly what Ctrl+C or a deadline produces
// mid-answer on the --no-tools (Completer.ChatStream) plain paths, where the
// partial text is only ever visible through the writer.
type plainInterruptedCompleter struct{ partial string }

func (plainInterruptedCompleter) Name() string { return "plain-interrupted" }
func (plainInterruptedCompleter) Chat(context.Context, provider.Request) (string, error) {
	return "", context.Canceled
}
func (c plainInterruptedCompleter) ChatStream(_ context.Context, _ provider.Request, w io.Writer) (string, error) {
	if w != nil {
		_, _ = io.WriteString(w, c.partial)
	}
	return "", context.Canceled
}
func (c plainInterruptedCompleter) ChatTurn(_ context.Context, req provider.Request) (*provider.Response, error) {
	if req.StreamWriter != nil {
		_, _ = io.WriteString(req.StreamWriter, c.partial)
	}
	return nil, context.Canceled
}

// assertInterruptedPlainPersisted verifies that both the user's question and
// the already-streamed partial answer survived into the given transcript.
func assertInterruptedPlainPersisted(t *testing.T, msgs []provider.Message, partial, question string) {
	t.Helper()
	var sawUser, sawPartial bool
	for _, msg := range msgs {
		switch msg.Role {
		case provider.RoleUser:
			if strings.Contains(msg.Content, question) {
				sawUser = true
			}
		case provider.RoleAssistant:
			if strings.Contains(msg.Content, partial) {
				sawPartial = true
			}
		}
	}
	if !sawUser {
		t.Error("the question the user asked is missing from the transcript")
	}
	if !sawPartial {
		t.Error("the answer the user already read is missing from the transcript")
	}
}

func TestPlainPersistenceErrorIgnoresOnlyStaleSaves(t *testing.T) {
	persistenceErr := errors.New("save failed")
	tests := []struct {
		name string
		err  error
		want error
	}{
		{name: "nil"},
		{name: "stale operation", err: ErrStaleOperation},
		{name: "stale autosave", err: ErrStaleAutosave},
		{name: "persistence", err: persistenceErr, want: persistenceErr},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := plainPersistenceError(test.err)
			if !errors.Is(got, test.want) || (got == nil) != (test.want == nil) {
				t.Fatalf("error = %v, want %v", got, test.want)
			}
		})
	}
}

// TestNoMessageLossInterruptedPlainContextTurnIsPersisted locks the
// no-message-loss guarantee for the durable-context plain path: the user's
// question and the answer they had already read on screen must both survive
// into the committed transcript. The context path previously returned the
// error without adopting the partial history, so restarting the session
// rebuilt a history missing both.
func TestNoMessageLossInterruptedPlainContextTurnIsPersisted(t *testing.T) {
	const partial = "Both fixes work. Here is the pro"
	const question = "prove it"
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sess := NewSession(&config.Resolved{ProviderName: "fake", Model: "model", SystemPrompt: "sys"}, plainInterruptedCompleter{partial: partial})
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
	reply, err := sess.SendUser(context.Background(), question, &sink)
	if err != nil {
		t.Fatalf("interrupted context plain turn must return the partial reply, got error: %v", err)
	}
	if !strings.Contains(reply, partial) {
		t.Fatalf("reply = %q, want it to contain %q", reply, partial)
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
