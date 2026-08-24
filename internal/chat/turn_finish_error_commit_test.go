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

	// Durable resume must see it too - a checkpoint must be committed to the
	// context store (tagged OutcomeUpstreamErr) so store.Load retrieves it.
	snapshot, err := store.Load(context.Background(), principal, principal.SessionID)
	if err != nil {
		t.Fatalf("Load checkpoint: %v", err)
	}
	var checkpointMsgs []provider.Message
	if err := contextstate.UnmarshalCanonical(snapshot.Active.ActiveContext, &checkpointMsgs); err != nil {
		t.Fatalf("UnmarshalCanonical: %v", err)
	}
	if !historyContains(checkpointMsgs, "does this survive a failed turn") {
		t.Fatal("errored turn's question is missing from durable checkpoint active context")
	}

	// LoadSession catalog projection also serves the updated state.
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

// TestErroredContextTurn_CompactedHistoryPreservesSummaryOnHardError verifies
// that when a turn triggers compaction during preparation and then hard-errors
// on the provider call, the compaction summary is committed to the durable
// checkpoint and live history rather than lost.
func TestErroredContextTurn_CompactedHistoryPreservesSummaryOnHardError(t *testing.T) {
	upstream := errors.New("upstream 500")
	sess, store, principal, manager := newErroredContextTurnFixture(t, erroringAgentCompleter{err: upstream})
	defer store.Close()

	// Seed multiple large messages so compaction triggers under a small budget.
	sess.mu.Lock()
	sess.Messages = []provider.Message{
		{Role: provider.RoleSystem, Content: "You are a helpful assistant."},
		{Role: provider.RoleUser, Content: strings.Repeat("pre-existing long user query ", 50)},
		{Role: provider.RoleAssistant, Content: strings.Repeat("pre-existing long assistant reply ", 50)},
		{Role: provider.RoleUser, Content: strings.Repeat("second long user query ", 50)},
		{Role: provider.RoleAssistant, Content: strings.Repeat("second long assistant reply ", 50)},
	}
	sess.mu.Unlock()

	summaryProvider := &chatSummaryProvider{}
	summarizer := plainSummarySummarizer(t, summaryProvider)
	manager.Summarizer = summarizer
	sess.SetSummarizer(summarizer)

	// Set a tight budget so preparation triggers compaction.
	sess.SetPromptBudget(300)

	var sink strings.Builder
	_, err := sess.SendUser(context.Background(), "question after compaction", &sink)
	if !errors.Is(err, upstream) {
		t.Fatalf("SendUser error = %v, want upstream error surfaced unchanged", err)
	}

	// 1. In-memory history must contain the user question and the injected summary.
	inMemory := sess.MessagesCopy()
	if !historyContains(inMemory, "question after compaction") {
		t.Fatal("question missing from in-memory history")
	}
	if !historyContains(inMemory, "summarized objective") {
		t.Fatal("injected compaction summary missing from in-memory history")
	}

	// 2. Durable checkpoint must carry the summary in active context.
	snapshot, err := store.Load(context.Background(), principal, principal.SessionID)
	if err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	var checkpointMsgs []provider.Message
	if err := contextstate.UnmarshalCanonical(snapshot.Active.ActiveContext, &checkpointMsgs); err != nil {
		t.Fatalf("UnmarshalCanonical: %v", err)
	}
	if !historyContains(checkpointMsgs, "question after compaction") {
		t.Fatal("question missing from checkpoint active context")
	}
	if !historyContains(checkpointMsgs, "summarized objective") {
		t.Fatal("injected compaction summary missing from checkpoint active context")
	}
}

// TestErroredContextTurn_InvalidShapeDiscardsGracefully verifies that when a turn
// fails with an unpairable tool-call shape (which cannot produce a valid commit
// request), finishErroredContextTurn falls back cleanly and drops pending admission.
func TestErroredContextTurn_InvalidShapeDiscardsGracefully(t *testing.T) {
	sess, store, principal, manager := newErroredContextTurnFixture(t, erroringAgentCompleter{})
	defer store.Close()

	token := sess.captureOperationToken("turn:invalid")
	if _, err := sess.StageToolAdmission([]string{"bash"}, token.TurnID); err != nil {
		t.Fatalf("StageToolAdmission: %v", err)
	}

	cfg := contextTurnConfig{manager: manager, principal: principal}
	// An unpairable history: tool call with no matching tool result.
	toolCall := provider.ToolCall{ID: "call-1", Type: "function"}
	toolCall.Function.Name = "bash"
	toolCall.Function.Arguments = "{}"
	loop := &agent.Loop{
		Messages: []provider.Message{
			{Role: provider.RoleSystem, Content: "sys"},
			{Role: provider.RoleUser, Content: "run tool"},
			{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{toolCall}},
		},
		HasPreparation: true,
	}

	upstreamErr := errors.New("upstream failed mid-tool")
	if err := sess.finishErroredContextTurn(context.Background(), loop, "run tool", token, nil, cfg, upstreamErr); err != nil {
		t.Fatalf("finishErroredContextTurn = %v, want nil", err)
	}

	if _, ok := sess.PendingAdmission(); ok {
		t.Fatal("pending admission must be dropped when commit fails")
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
