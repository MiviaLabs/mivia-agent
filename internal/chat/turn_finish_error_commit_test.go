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
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// erroringAgentCompleter fails every ChatTurn call with a plain, non-interrupt,
// non-budget error before ever appending an assistant or tool message - the
// turn's history stays exactly the pre-existing history plus the new user
// message, a structurally valid shape.
type erroringAgentCompleter struct{ err error }

func (erroringAgentCompleter) Name() string { return "erroring-agent" }
func (c erroringAgentCompleter) Chat(context.Context, provider.Request) (string, error) {
	return "", c.err
}
func (c erroringAgentCompleter) ChatStream(context.Context, provider.Request, io.Writer) (string, error) {
	return "", c.err
}
func (c erroringAgentCompleter) ChatTurn(context.Context, provider.Request) (*provider.Response, error) {
	return nil, c.err
}

// newErroredContextTurnFixture builds an agent-mode (UseTools=true), durable
// context-store-backed session, matching interrupted_turn_persist_test.go's
// TestNoMessageLossInterruptedPlainContextTurnIsPersisted setup but on the
// sendAgent path finishContextTurn actually serves.
func newErroredContextTurnFixture(t *testing.T, completer provider.Completer) (*Session, *storage.SQLite, contextstate.Principal, *contextmgr.ContextManager) {
	t.Helper()
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	sess := NewSession(&config.Resolved{ProviderName: "fake", Model: "model", SystemPrompt: "sys"}, completer)
	sess.UseTools = true
	sess.Tools = tools.NewRegistry()
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
	return sess, store, principal, manager
}

// TestErroredContextTurn_ValidShapeCommitsDurably pins the F3 fix: a
// non-interrupted, structurally valid errored turn must not vanish from
// resume - the user's question has to survive into the durably committed
// checkpoint, not just the in-memory session.
func TestErroredContextTurn_ValidShapeCommitsDurably(t *testing.T) {
	upstream := errors.New("upstream 500")
	sess, store, principal, _ := newErroredContextTurnFixture(t, erroringAgentCompleter{err: upstream})
	defer store.Close()

	var sink strings.Builder
	_, err := sess.SendUser(context.Background(), "does this survive a failed turn", &sink)
	if !errors.Is(err, upstream) {
		t.Fatalf("SendUser error = %v, want the original upstream error surfaced unchanged", err)
	}

	// In-memory history carries the question (same guarantee interrupted
	// turns already had).
	if !historyContains(sess.MessagesCopy(), "does this survive a failed turn") {
		t.Fatal("errored turn's question is missing from in-memory history")
	}

	// Durable resume must see it too - this is the actual regression the fix
	// closes: before F3, finishContextTurn's error branch never called Commit
	// at all, so this Load would come back with none of this turn's content.
	payload, info, err := store.LoadSession(context.Background(), principal, principal.SessionID)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if info.SessionID != principal.SessionID {
		t.Fatalf("info.SessionID = %q, want the live session id %q", info.SessionID, principal.SessionID)
	}
	if !strings.Contains(string(payload), "does this survive a failed turn") {
		t.Fatalf("durable payload = %s, want it to contain the errored turn's question", payload)
	}
}

// TestErroredContextTurn_BudgetExceededStillDiscards is defense-in-depth: even
// though ErrPromptBudgetExceeded cannot currently reach finishContextTurn with
// a PreparationManager configured (sdkPromptBudgetPreflight is a no-op in
// that configuration), the guard exists and must keep discarding rather than
// committing an over-budget history.
func TestErroredContextTurn_BudgetExceededStillDiscards(t *testing.T) {
	sess, store, principal, manager := newErroredContextTurnFixture(t, erroringAgentCompleter{})
	defer store.Close()
	sess.mu.Lock()
	sess.Messages = []provider.Message{{Role: provider.RoleSystem, Content: "sys"}, {Role: provider.RoleUser, Content: "hello"}}
	sess.mu.Unlock()
	token := sess.captureOperationToken("turn:budget")
	cfg := contextTurnConfig{manager: manager, principal: principal}
	loop := &agent.Loop{Messages: sess.MessagesCopy()}
	if err := sess.finishErroredContextTurn(context.Background(), loop, "hello", token, nil, cfg, agent.ErrPromptBudgetExceeded); err != nil {
		t.Fatalf("finishErroredContextTurn = %v, want nil", err)
	}
	payload, _, err := store.LoadSession(context.Background(), principal, principal.SessionID)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if strings.Contains(string(payload), "hello") {
		t.Fatalf("payload = %s, want the budget-exceeded turn discarded, not committed", payload)
	}
}

func historyContains(msgs []provider.Message, substr string) bool {
	for _, m := range msgs {
		if strings.Contains(m.Content, substr) {
			return true
		}
	}
	return false
}
