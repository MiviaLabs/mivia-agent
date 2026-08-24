package chat

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
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

// TestNoMessageLossErroredPlainLegacyTurnIsPersisted pins the fix for the
// legacy (no session/context store) plain path: sendPlainLegacy previously
// discarded a non-interrupted error's history entirely ("Non-interrupted
// errors keep today's drop-everything behavior"); the user's question and
// any already-streamed partial reply must survive into the session's
// in-memory history and the on-disk autosave, while the original error must
// still surface to the caller exactly as before.
func TestNoMessageLossErroredPlainLegacyTurnIsPersisted(t *testing.T) {
	upstream := errors.New("upstream 500")
	const partial = "Both fixes work. Here is the pro"
	const question = "prove it"
	dir := t.TempDir()
	store, err := NewFileSessionStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	sess := NewSession(&config.Resolved{Model: "test-model", SystemPrompt: "sys"}, erroringPlainCompleter{partial: partial, err: upstream})
	sess.SetSessionStore(store, NewSaveManager(store, "test-model", "test-provider"))

	var sink strings.Builder
	_, sendErr := sess.SendUser(context.Background(), question, &sink)
	if !errors.Is(sendErr, upstream) {
		t.Fatalf("SendUser error = %v, want the original upstream error surfaced unchanged", sendErr)
	}

	assertInterruptedPlainPersisted(t, sess.MessagesCopy(), partial, question)

	names, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) == 0 {
		t.Fatal("errored turn was never persisted: no session on disk")
	}
	loaded, err := store.Load(names[0].Name)
	if err != nil {
		t.Fatal(err)
	}
	assertInterruptedPlainPersisted(t, loaded, partial, question)
}

// TestErroredPlainLegacyTurnBudgetExceededStillDiscards is defense-in-depth,
// mirroring finishErroredContextTurn's identical guard on the agent path: an
// over-budget history must never be adopted or persisted even if some future
// completer implementation raises ErrPromptBudgetExceeded from ChatStream
// itself rather than the pre-flight check in sendPlainLegacy.
func TestErroredPlainLegacyTurnBudgetExceededStillDiscards(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileSessionStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	sess := NewSession(&config.Resolved{Model: "test-model", SystemPrompt: "sys"}, erroringPlainCompleter{err: agent.ErrPromptBudgetExceeded})
	sess.SetSessionStore(store, NewSaveManager(store, "test-model", "test-provider"))

	var sink strings.Builder
	if _, err := sess.SendUser(context.Background(), "too much history", &sink); !errors.Is(err, agent.ErrPromptBudgetExceeded) {
		t.Fatalf("SendUser error = %v, want ErrPromptBudgetExceeded surfaced unchanged", err)
	}
	names, _ := store.List()
	if len(names) != 0 {
		t.Fatal("budget-exceeded turn must not be persisted")
	}
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
