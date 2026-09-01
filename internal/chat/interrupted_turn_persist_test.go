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

// plainStreamingCompleter streams its answer into the writer, exactly like a
// real provider completer does and unlike fakeCompleter (which returns the
// full response without ever touching req.StreamWriter) - needed to prove
// the answer really does reach the caller's writer before a later commit
// failure, not just that fakeCompleter's own Content field held it.
type plainStreamingCompleter struct{ answer string }

func (plainStreamingCompleter) Name() string { return "plain-streaming" }
func (c plainStreamingCompleter) Chat(context.Context, provider.Request) (string, error) {
	return c.answer, nil
}
func (c plainStreamingCompleter) ChatStream(_ context.Context, _ provider.Request, w io.Writer) (string, error) {
	if w != nil {
		_, _ = io.WriteString(w, c.answer)
	}
	return c.answer, nil
}
func (c plainStreamingCompleter) ChatTurn(_ context.Context, req provider.Request) (*provider.Response, error) {
	if req.StreamWriter != nil {
		_, _ = io.WriteString(req.StreamWriter, c.answer)
	}
	return &provider.Response{Content: c.answer, FinishReason: "stop"}, nil
}

// plainCommitFailurePublisher makes the context manager's durable commit
// fail unconditionally - the context-catalog counterpart of the legacy
// countingFailureStore this file used to seed a persistence failure before
// the file-backed session store it wrapped was removed.
type plainCommitFailurePublisher struct{ err error }

func (p plainCommitFailurePublisher) Commit(context.Context, contextmgr.Preparation, contextmgr.TurnResult) error {
	return p.err
}

// TestCompletedPlainContextTurnCommitFailureDropsTheTurn and
// TestInterruptedPlainContextTurnCommitFailureDropsTheTurn pin CURRENT
// behavior - they are not an endorsement of it. The legacy tests these
// replace (TestCompletedPlainLegacyTurnReportsAutosaveFailure,
// TestInterruptedPlainLegacyTurnReportsAutosaveFailure) proved a genuine
// no-message-loss guarantee: a SaveManager autosave failure reported
// ErrPersistence while the reply and the exchange survived in memory, so the
// user could keep chatting or retry the save. commitPlainContextTurn and
// commitInterruptedPlainContext (context_integration_turn.go) do not carry
// that guarantee forward: on a commit failure they return an EMPTY reply and
// the raw commit error, and never adopt the exchange into s.Messages at all.
// The model's answer was already streamed to the caller's writer in real
// time (plainTurnStreamWriter multi-writes it before the commit ever runs),
// so an interactive caller still sees it on screen, but a one-shot (-p)
// caller reading only the RETURNED reply gets nothing, and the exchange is
// gone from the next turn's context and from /save either way. See the
// spawned follow-up task for restoring the guarantee on this path; these
// tests exist so that fix has a red/green marker, and so an accidental
// further regression (e.g. the writer stops getting the streamed text
// either) is still caught in the meantime.
func TestCompletedPlainContextTurnCommitFailureDropsTheTurn(t *testing.T) {
	const answer = "Both fixes work."
	const question = "prove it"
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sess := NewSession(&config.Resolved{ProviderName: "fake", Model: "model", SystemPrompt: "sys"}, plainStreamingCompleter{answer: answer})
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
	want := errors.New("disk full")
	manager := &contextmgr.ContextManager{
		PreparationManager:  contextmgr.StructuralPreparationManager{},
		CheckpointPublisher: plainCommitFailurePublisher{err: want},
		Enabled:             true,
	}
	if err := sess.SetContextManager(manager, principal); err != nil {
		t.Fatal(err)
	}
	sess.SetContextStore(store)
	before := sess.contextRevision

	var sink strings.Builder
	reply, err := sess.SendUser(context.Background(), question, &sink)
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want the commit failure", err)
	}
	if reply != "" {
		t.Fatalf("reply = %q, want empty - a commit failure loses the returned reply on the current context-plain path", reply)
	}
	if !strings.Contains(sink.String(), answer) {
		t.Fatalf("caller's writer = %q, want it to still contain the streamed answer even though the commit failed", sink.String())
	}
	for _, m := range sess.MessagesCopy() {
		if strings.Contains(m.Content, question) || strings.Contains(m.Content, answer) {
			t.Fatalf("messages = %+v, want the exchange absent from memory - a commit failure adopts nothing on this path", sess.MessagesCopy())
		}
	}
	if got := sess.contextRevision; got != before {
		t.Fatalf("revision = %+v, want unchanged %+v", got, before)
	}
}

func TestInterruptedPlainContextTurnCommitFailureDropsTheTurn(t *testing.T) {
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
	want := errors.New("disk full")
	manager := &contextmgr.ContextManager{
		PreparationManager:  contextmgr.StructuralPreparationManager{},
		CheckpointPublisher: plainCommitFailurePublisher{err: want},
		Enabled:             true,
	}
	if err := sess.SetContextManager(manager, principal); err != nil {
		t.Fatal(err)
	}
	sess.SetContextStore(store)
	before := sess.contextRevision

	var sink strings.Builder
	reply, err := sess.SendUser(context.Background(), question, &sink)
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want the commit failure", err)
	}
	if reply != "" {
		t.Fatalf("reply = %q, want empty - a commit failure loses the returned reply on the current context-plain path", reply)
	}
	if !strings.Contains(sink.String(), partial) {
		t.Fatalf("caller's writer = %q, want it to still contain the streamed partial even though the commit failed", sink.String())
	}
	for _, m := range sess.MessagesCopy() {
		if strings.Contains(m.Content, question) || strings.Contains(m.Content, partial) {
			t.Fatalf("messages = %+v, want the exchange absent from memory - a commit failure adopts nothing on this path", sess.MessagesCopy())
		}
	}
	if got := sess.contextRevision; got != before {
		t.Fatalf("revision = %+v, want unchanged %+v", got, before)
	}
}
