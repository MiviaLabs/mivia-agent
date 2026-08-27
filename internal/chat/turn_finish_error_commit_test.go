package chat

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"reflect"
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

// emptyThenRealAgentCompleter simulates a provider that returns a genuinely
// empty response (no content, no tool calls, no error - the exact shape
// RequireFinalText turns into "agent: turn produced no assistant text")
// until the test flips returnReal, then a normal answer on every call
// after. A bool switch rather than a call-count threshold, because
// RunAgentLoopOnce's bounded empty-response retry (Part 3, agentloop_run.go's
// retryOnEmptyResponse) means a single SendUser call can itself invoke
// ChatTurn multiple times - a fixed count could not reliably tell "still
// exhausting this turn's retries" apart from "the next turn started". Used
// to prove the empty-response repair (provider.DropEmptyAssistantTurns,
// wired into finishAgentTurn) keeps the poisoned shape out of persisted
// history so the NEXT turn's Prepare() does not also fail.
type emptyThenRealAgentCompleter struct {
	calls      int
	returnReal bool
}

func (c *emptyThenRealAgentCompleter) Name() string { return "empty-then-real" }
func (c *emptyThenRealAgentCompleter) Chat(context.Context, provider.Request) (string, error) {
	return "", nil
}
func (c *emptyThenRealAgentCompleter) ChatStream(context.Context, provider.Request, io.Writer) (string, error) {
	return "", nil
}
func (c *emptyThenRealAgentCompleter) ChatTurn(context.Context, provider.Request) (*provider.Response, error) {
	c.calls++
	if !c.returnReal {
		return &provider.Response{}, nil
	}
	return &provider.Response{Content: "a real answer"}, nil
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

// TestErroredContextTurn_EmptyResponseWithNoPreparationDoesNotCorruptHistory
// pins the adoptFailedTurnSnapshot fix: a turn that never got far enough to
// build a preparation (loop.HasPreparation == false), and whose candidate
// history ends in a provider-shape-invalid message (empty content, no tool
// calls - exactly what a provider's empty "no assistant text" response
// leaves behind), must not overwrite the live session or the durable
// snapshot. Before the fix, adoptFailedTurnSnapshot adopted this candidate
// unconditionally, durably persisting the corruption; since Prepare()
// validates the same shape on every later turn, this poisoned every
// subsequent turn on the session (matching the reported symptom: repeated
// "turn produced no assistant text" failures that end in a resumed session
// missing its prior history).
func TestErroredContextTurn_EmptyResponseWithNoPreparationDoesNotCorruptHistory(t *testing.T) {
	sess, store, principal, manager := newErroredContextTurnFixture(t, erroringAgentCompleter{})
	defer store.Close()

	goodHistory := []provider.Message{
		{Role: provider.RoleSystem, Content: "sys"},
		{Role: provider.RoleUser, Content: "first real question"},
		{Role: provider.RoleAssistant, Content: "first real answer"},
	}
	sess.mu.Lock()
	sess.Messages = append([]provider.Message(nil), goodHistory...)
	sess.mu.Unlock()

	beforePayload, _, err := store.LoadSession(context.Background(), principal, principal.SessionID)
	if err != nil {
		t.Fatalf("LoadSession (before): %v", err)
	}

	token := sess.captureOperationToken("turn:empty-response")
	if _, err := sess.StageToolAdmission([]string{"bash"}, token.TurnID); err != nil {
		t.Fatalf("StageToolAdmission: %v", err)
	}
	cfg := contextTurnConfig{manager: manager, principal: principal}
	// Candidate carries the good history plus a new user turn and a
	// provider-shape-invalid trailing assistant message: empty content, no
	// tool calls. HasPreparation is false, matching a turn whose Prepare()
	// failed before any provider call - the exact state a poisoned
	// committed history produces on every subsequent turn.
	candidate := append(append([]provider.Message(nil), goodHistory...),
		provider.Message{Role: provider.RoleUser, Content: "continue"},
		provider.Message{Role: provider.RoleAssistant, Content: ""},
	)
	loop := &agent.Loop{Messages: candidate, HasPreparation: false}

	turnErr := errors.New("agent: turn produced no assistant text")
	if err := sess.finishErroredContextTurn(context.Background(), loop, "continue", token, nil, cfg, turnErr); err != nil {
		t.Fatalf("finishErroredContextTurn = %v, want nil", err)
	}

	if _, ok := sess.PendingAdmission(); ok {
		t.Fatal("pending admission must be dropped when a shape-invalid snapshot is rejected")
	}

	if got := sess.MessagesCopy(); !reflect.DeepEqual(got, goodHistory) {
		t.Fatalf("in-memory history mutated by a shape-invalid failed-turn snapshot: got %+v, want %+v", got, goodHistory)
	}

	afterPayload, _, err := store.LoadSession(context.Background(), principal, principal.SessionID)
	if err != nil {
		t.Fatalf("LoadSession (after): %v", err)
	}
	if string(afterPayload) != string(beforePayload) {
		t.Fatalf("durable snapshot changed from a shape-invalid failed-turn adoption:\nbefore=%s\nafter=%s", beforePayload, afterPayload)
	}
}

// TestErroredContextTurn_ValidNoPreparationSnapshotStillAdopts is the
// companion to the corruption-guard test above: a structurally VALID
// candidate (no dangling tool calls, no empty assistant turns) with
// HasPreparation == false must still be adopted as before - the new
// validation must reject only genuinely invalid shapes, not every
// no-preparation failure.
func TestErroredContextTurn_ValidNoPreparationSnapshotStillAdopts(t *testing.T) {
	sess, store, principal, manager := newErroredContextTurnFixture(t, erroringAgentCompleter{})
	defer store.Close()

	token := sess.captureOperationToken("turn:valid-no-prep")
	cfg := contextTurnConfig{manager: manager, principal: principal}
	candidate := []provider.Message{
		{Role: provider.RoleSystem, Content: "sys"},
		{Role: provider.RoleUser, Content: "hello there"},
	}
	loop := &agent.Loop{Messages: candidate, HasPreparation: false}

	turnErr := errors.New("some pre-preparation failure")
	if err := sess.finishErroredContextTurn(context.Background(), loop, "hello there", token, nil, cfg, turnErr); err != nil {
		t.Fatalf("finishErroredContextTurn = %v, want nil", err)
	}

	if !historyContains(sess.MessagesCopy(), "hello there") {
		t.Fatal("valid no-preparation snapshot was not adopted into in-memory history")
	}
	payload, _, err := store.LoadSession(context.Background(), principal, principal.SessionID)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if !strings.Contains(string(payload), "hello there") {
		t.Fatalf("payload = %s, want the valid no-preparation snapshot durably saved", payload)
	}
}

// TestFinishAgentTurn_EmptyResponseDoesNotPoisonNextTurnsPreparation is the
// end-to-end companion to the adoptFailedTurnSnapshot guard above: it drives
// a real empty-response turn through the full SendUser -> sendAgent ->
// finishAgentTurn path (not a hand-built loop.Messages), confirming
// provider.DropEmptyAssistantTurns keeps the poisoned shape out of committed
// history so the immediately following turn's Prepare() succeeds instead of
// failing the same way every time - the actual reported symptom (repeated
// identical "turn produced no assistant text" failures on every retry).
func TestFinishAgentTurn_EmptyResponseDoesNotPoisonNextTurnsPreparation(t *testing.T) {
	completer := &emptyThenRealAgentCompleter{}
	sess, store, _, _ := newErroredContextTurnFixture(t, completer)
	defer store.Close()

	var sink strings.Builder
	_, err := sess.SendUser(context.Background(), "first question", &sink)
	if err == nil || !strings.Contains(err.Error(), "no assistant text") {
		t.Fatalf("first turn error = %v, want a 'no assistant text' failure", err)
	}

	// The poisoned shape must never have reached history: if it had,
	// contextmgr's planner would reject it on the very next Prepare() call
	// with a message-shape error, not the completer's real answer.
	for _, m := range sess.MessagesCopy() {
		if m.Role == provider.RoleAssistant && len(m.ToolCalls) == 0 && strings.TrimSpace(m.Content) == "" {
			t.Fatalf("empty assistant turn survived into committed history: %+v", sess.MessagesCopy())
		}
	}

	completer.returnReal = true
	sink.Reset()
	reply, err := sess.SendUser(context.Background(), "second question", &sink)
	if err != nil {
		t.Fatalf("second turn failed - history was poisoned by the first: %v", err)
	}
	if !strings.Contains(reply, "a real answer") {
		t.Fatalf("unexpected reply: %q", reply)
	}
}

// TestFinishAgentTurn_LegacyPathStripsEmptyAssistantMessage proves Part 2's
// necessity independent of the durable-context guard added for Part 1:
// commitPreparedTurn (the legacy, non-context-managed persistence path,
// turn_finish.go's `s.Messages = msgs` with no shape check on any branch)
// has no protection of its own. Without provider.DropEmptyAssistantTurns
// running unconditionally at turn adoption (finishAgentTurn, before the
// context-managed/legacy branch), an empty-response turn would durably
// persist a shape that later poisons any context-enabled resume of the same
// history, or simply accumulates in a legacy session across repeated
// retries.
func TestFinishAgentTurn_LegacyPathStripsEmptyAssistantMessage(t *testing.T) {
	completer := &emptyThenRealAgentCompleter{}
	sess := NewSession(&config.Resolved{ProviderName: "fake", Model: "model", SystemPrompt: "sys"}, completer)
	sess.Tools = tools.NewRegistry()
	sess.UseTools = true

	var sink strings.Builder
	_, err := sess.SendUser(context.Background(), "first question", &sink)
	if err == nil || !strings.Contains(err.Error(), "no assistant text") {
		t.Fatalf("first turn error = %v, want a 'no assistant text' failure", err)
	}

	for _, m := range sess.MessagesCopy() {
		if m.Role == provider.RoleAssistant && len(m.ToolCalls) == 0 && strings.TrimSpace(m.Content) == "" {
			t.Fatalf("empty assistant turn survived into the legacy session's persisted history: %+v", sess.MessagesCopy())
		}
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
