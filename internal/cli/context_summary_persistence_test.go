package cli

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// activeCarriesSummary reports whether the session's live active context
// carries the rendered context-summary message.
func activeCarriesSummary(messages []provider.Message) bool {
	for _, msg := range messages {
		if msg.Name == "context-summary" || strings.Contains(msg.Content, "[host-injected context summary") {
			return true
		}
	}
	return false
}

// TestAutoCompactionSummarySurvivesTheTurnBoundary is the load-bearing case
// for automatic compaction. injectSummary renders the summary into an
// EPHEMERAL clone of the request messages and is gated on
// l.LastPreparation.Compacted. A fresh agent.Loop is built per turn
// (chat/session.go), so that flag is false on the next turn and the summary is
// never injected again. The compacted history the turn commits does not carry
// it either.
//
// The consequence: a turn that compacts drops the old messages permanently,
// shows the model a summary of them for the rest of that turn only, and then
// the summary evaporates at the turn boundary. Every later turn sees the
// truncated history with no account of what was removed.
//
// The manual /compact path already solved exactly this - it appends the
// rendered summary to both s.Messages and the committed active set, because
// "the checkpoint is its only durable carrier" (chat/context_control.go).
// Automatic compaction never got the same treatment.
func TestAutoCompactionSummarySurvivesTheTurnBoundary(t *testing.T) {
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	res := summaryWiringResolved(t, true)
	completer := &summaryScriptedCompleter{}
	session := chat.NewSession(res, completer)
	if _, err := configureSessionContext(session, t.TempDir(), store, res); err != nil {
		t.Fatal(err)
	}

	driveCompactingTurn(t, session)

	// The compacting turn itself must have shown the model a summary,
	// otherwise this test is not exercising what it claims to.
	if !requestCarriesSummary(completer.allRequests()) {
		t.Fatal("the compacting turn never injected a summary; harness precondition failed")
	}

	// After the compacting turn commits, the summary must still be reachable:
	// the model has lost the dropped messages and this is its only account of
	// them.
	if !activeCarriesSummary(session.MessagesCopy()) {
		t.Error("compacted active context carries no summary of the dropped messages")
	}

	// And it must reach the wire on the NEXT turn, which is the only thing
	// the model actually sees.
	before := len(completer.allRequests())
	if _, err := session.SendUser(context.Background(), "third question", io.Discard); err != nil {
		t.Fatal(err)
	}
	after := completer.allRequests()
	if len(after) <= before {
		t.Fatal("third turn sent no request")
	}
	var turnRequests []provider.Request
	for _, req := range after[before:] {
		if !isSummaryRequest(req) {
			turnRequests = append(turnRequests, req)
		}
	}
	if len(turnRequests) == 0 {
		t.Fatal("third turn sent no non-summary request")
	}
	if !requestCarriesSummary(turnRequests) {
		t.Fatal("the turn after an automatic compaction carries no summary: " +
			"the model has lost both the dropped messages and any account of them")
	}
}

// TestAgentLoopCompactionSummarySurvivesTheTurnBoundary is the same contract
// on the tool-enabled path, which is the one an interactive session and the
// desktop app actually run. The plain path and the agent loop have entirely
// separate injectors (chat/summary_inject.go and agent/summary_inject.go) and
// separate commit paths, so each needs its own proof: fixing one says nothing
// about the other.
func TestAgentLoopCompactionSummarySurvivesTheTurnBoundary(t *testing.T) {
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	res := summaryWiringResolved(t, true)
	completer := &summaryScriptedCompleter{}
	session := chat.NewSession(res, completer)
	if _, err := configureSessionContext(session, t.TempDir(), store, res); err != nil {
		t.Fatal(err)
	}
	session.UseTools = true
	session.Tools = tools.NewRegistry()
	if !session.AgentTurnEnabled() {
		t.Fatal("session did not take the agent-loop path")
	}

	driveCompactingTurn(t, session)

	if !requestCarriesSummary(completer.allRequests()) {
		t.Fatal("the compacting agent turn never injected a summary; harness precondition failed")
	}
	if !activeCarriesSummary(session.MessagesCopy()) {
		t.Error("compacted active context carries no summary of the dropped messages")
	}

	before := len(completer.allRequests())
	if _, err := session.SendUser(context.Background(), "third question", io.Discard); err != nil {
		t.Fatal(err)
	}
	var turnRequests []provider.Request
	for _, req := range completer.allRequests()[before:] {
		if !isSummaryRequest(req) {
			turnRequests = append(turnRequests, req)
		}
	}
	if len(turnRequests) == 0 {
		t.Fatal("third turn sent no non-summary request")
	}
	if !requestCarriesSummary(turnRequests) {
		t.Fatal("the agent turn after an automatic compaction carries no summary")
	}
}

// TestPlainCompactionEmitsTypedEventToTheTurnCallback pins the signal a
// --json consumer (the desktop sidecar) needs to know a compaction happened.
// sendUserWithTurn passed the turn's onEvent only to the agent loop and
// dropped it entirely on the plain (--no-tools) path, so
// emitContextCompaction read a nil s.OnAgentEvent and a real automatic
// compaction reached nobody: no "compaction" NDJSON line, no TUI banner. The
// event type, its wire mapping, and the /compact path were all tested; the
// automatic path that actually produces most compactions was not.
func TestPlainCompactionEmitsTypedEventToTheTurnCallback(t *testing.T) {
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	res := summaryWiringResolved(t, true)
	completer := &summaryScriptedCompleter{}
	session := chat.NewSession(res, completer)
	if _, err := configureSessionContext(session, t.TempDir(), store, res); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var compactions []agent.Event
	collect := func(ev agent.Event) {
		if ev.Kind == agent.EventCompaction {
			mu.Lock()
			compactions = append(compactions, ev)
			mu.Unlock()
		}
	}

	if _, err := session.SendUserWithEvent(context.Background(), "first "+strings.Repeat("x", 2000), io.Discard, collect); err != nil {
		t.Fatal(err)
	}
	next := "second question"
	cost, err := provider.EstimatePromptCost(append(session.MessagesCopy(), provider.Message{Role: provider.RoleUser, Content: next}), nil, provider.ContextAccountingProfile{})
	if err != nil {
		t.Fatal(err)
	}
	if err := session.SetPromptBudget(cost); err != nil {
		t.Fatal(err)
	}
	if _, err := session.SendUserWithEvent(context.Background(), next, io.Discard, collect); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(compactions) == 0 {
		t.Fatal("automatic compaction on the plain path emitted no compaction event to the turn callback")
	}
	typed := compactions[0].Compaction
	if typed == nil {
		t.Fatal("compaction event carried no typed record")
	}
	if typed.BeforeTokens <= typed.AfterTokens {
		t.Fatalf("compaction record reports no reduction: %d -> %d", typed.BeforeTokens, typed.AfterTokens)
	}
}
