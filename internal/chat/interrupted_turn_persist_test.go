package chat

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// interruptedCompleter streams a partial answer, then reports the turn was
// cancelled - what Ctrl+C or a request deadline produces mid-answer.
type interruptedCompleter struct{ partial string }

func (interruptedCompleter) Name() string { return "interrupted" }
func (interruptedCompleter) Chat(context.Context, provider.Request) (string, error) {
	return "", context.Canceled
}
func (interruptedCompleter) ChatStream(_ context.Context, _ provider.Request, _ io.Writer) (string, error) {
	return "", context.Canceled
}
func (c interruptedCompleter) ChatTurn(_ context.Context, req provider.Request) (*provider.Response, error) {
	if req.StreamWriter != nil {
		_, _ = io.WriteString(req.StreamWriter, c.partial)
	}
	return nil, context.Canceled
}

// TestNoMessageLossInterruptedTurnIsPersisted locks the defect where an errored
// or cancelled turn was never written to disk: SaveAfterTurn sat below an early
// `if err != nil { return }`. The user's question and the answer they had already
// read on screen both vanished from the transcript, so restarting the session
// rebuilt a history missing both - and the model repeated itself.
func TestNoMessageLossInterruptedTurnIsPersisted(t *testing.T) {
	const partial = "Both fixes work. Here is the pro"
	dir := t.TempDir()
	store, err := NewFileSessionStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	sess := NewSession(&config.Resolved{
		Model:        "test-model",
		SystemPrompt: "sys",
	}, interruptedCompleter{partial: partial})
	sess.Tools = tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})
	sess.UseTools = true
	sess.SetSessionStore(store, NewSaveManager(store, "test-model", "test-provider"))

	var sink strings.Builder
	if _, err := sess.SendUser(context.Background(), "prove it", &sink); err == nil {
		t.Fatal("cancelled turn must still report its error")
	}

	names, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) == 0 {
		t.Fatal("cancelled turn was never persisted: no session on disk")
	}

	loaded, err := store.Load(names[0].Name)
	if err != nil {
		t.Fatal(err)
	}
	var sawUser, sawPartial bool
	for _, msg := range loaded {
		switch msg.Role {
		case provider.RoleUser:
			if strings.Contains(msg.Content, "prove it") {
				sawUser = true
			}
		case provider.RoleAssistant:
			if strings.Contains(msg.Content, partial) {
				sawPartial = true
			}
		}
	}
	if !sawUser {
		t.Error("the question the user asked is missing from the persisted transcript")
	}
	if !sawPartial {
		t.Error("the answer the user already read is missing from the persisted transcript")
	}
}

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

// TestNoMessageLossInterruptedPlainLegacyTurnIsPersisted locks the
// no-message-loss guarantee for the legacy (--no-tools) autosave path: the
// user's question and the answer they had already read on screen must both
// survive into the transcript. The legacy path previously returned the error
// without adopting the partial history, so restarting the session rebuilt a
// history missing both.
func TestNoMessageLossInterruptedPlainLegacyTurnIsPersisted(t *testing.T) {
	const partial = "Both fixes work. Here is the pro"
	const question = "prove it"
	dir := t.TempDir()
	store, err := NewFileSessionStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	sess := NewSession(&config.Resolved{Model: "test-model", SystemPrompt: "sys"}, plainInterruptedCompleter{partial: partial})
	sess.SetSessionStore(store, NewSaveManager(store, "test-model", "test-provider"))

	var sink strings.Builder
	reply, err := sess.SendUser(context.Background(), question, &sink)
	if err != nil {
		t.Fatalf("interrupted plain turn must return the partial reply, got error: %v", err)
	}
	if !strings.Contains(reply, partial) {
		t.Fatalf("reply = %q, want it to contain %q", reply, partial)
	}

	assertInterruptedPlainPersisted(t, sess.MessagesCopy(), partial, question)

	names, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) == 0 {
		t.Fatal("interrupted plain turn was never persisted: no session on disk")
	}
	loaded, err := store.Load(names[0].Name)
	if err != nil {
		t.Fatal(err)
	}
	assertInterruptedPlainPersisted(t, loaded, partial, question)
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
