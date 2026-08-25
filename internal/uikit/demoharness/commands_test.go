package demoharness

import (
	"context"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

func newHarness(t *testing.T) *Harness {
	t.Helper()
	h, err := New("smalltalk", 0)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestRunTheme(t *testing.T) {
	h := newHarness(t)
	got := h.Run(context.Background(), "theme", "")
	if !got.OpenTheme {
		t.Errorf("got %+v, want OpenTheme", got)
	}
}

func TestRunHelp(t *testing.T) {
	h := newHarness(t)
	got := h.Run(context.Background(), "help", "")
	if !got.OpenHelp {
		t.Errorf("got %+v, want OpenHelp", got)
	}
}

func TestRunQueue(t *testing.T) {
	h := newHarness(t)
	got := h.Run(context.Background(), "queue", "")
	if !got.OpenQueue {
		t.Errorf("got %+v, want OpenQueue", got)
	}
}

func TestRunModelReturnsChoices(t *testing.T) {
	h := newHarness(t)
	got := h.Run(context.Background(), "model", "")
	if len(got.ModelChoiceGroups) == 0 {
		t.Fatalf("got no groups, want at least one: %+v", got)
	}
	var total int
	for _, g := range got.ModelChoiceGroups {
		total += len(g.Models)
	}
	if total != len(demoModels) {
		t.Errorf("got %d models across groups, want %d: %+v", total, len(demoModels), got)
	}
}

func TestRunEffortReturnsChoices(t *testing.T) {
	h := newHarness(t)
	got := h.Run(context.Background(), "effort", "")
	if len(got.EffortChoices) == 0 {
		t.Fatalf("got no effort choices, want at least one: %+v", got)
	}
	sel := h.SelectEffort(context.Background(), "high")
	if !strings.Contains(sel.Notice, "high") {
		t.Errorf("got notice %q, want it to contain 'high'", sel.Notice)
	}
}

func TestRunContext(t *testing.T) {
	h := newHarness(t)
	got := h.Run(context.Background(), "context", "")
	if !strings.Contains(got.Notice, "Context usage") {
		t.Errorf("got notice %q, want it to describe context usage", got.Notice)
	}
}

func TestRunCost(t *testing.T) {
	h := newHarness(t)
	got := h.Run(context.Background(), "cost", "")
	if !strings.Contains(got.Notice, "Session cost") {
		t.Errorf("got notice %q, want it to describe the session cost", got.Notice)
	}
}

func TestRunAgentsOffersTheRosterAsChoices(t *testing.T) {
	h := newHarness(t)
	got := h.Run(context.Background(), "agents", "")
	if got.Notice != "" || got.Err != "" {
		t.Errorf("got %q/%q, want a pure picker outcome", got.Notice, got.Err)
	}
	if len(got.AgentChoices) != len(demoAgents) {
		t.Fatalf("got %d choices, want %d (one per roster entry)", len(got.AgentChoices), len(demoAgents))
	}
	if got.AgentChoices[0] != ports.DefaultAgentName {
		t.Errorf("first choice is %q, want the default orchestrator %q", got.AgentChoices[0], ports.DefaultAgentName)
	}
}

// TestSelectAgentSwitchesAndRejectsUnknown: a roster name switches the
// session agent; an unknown name is an error, never a silent no-op.
func TestSelectAgentSwitchesAndRejectsUnknown(t *testing.T) {
	h := newHarness(t)
	got := h.SelectAgent(context.Background(), ports.DefaultAgentName)
	if got.Err != "" || !strings.Contains(got.Notice, ports.DefaultAgentName) {
		t.Errorf("select default: got %q/%q", got.Err, got.Notice)
	}
	if got := h.SelectAgent(context.Background(), "nope"); got.Err == "" {
		t.Errorf("unknown agent selected silently: %+v", got)
	}
}

func TestRunResumeOffersSessions(t *testing.T) {
	h := newHarness(t)
	got := h.Run(context.Background(), "resume", "")
	if got.Notice != "" || got.Err != "" {
		t.Errorf("got %q/%q, want a pure picker outcome", got.Notice, got.Err)
	}
	if len(got.SessionChoices) != len(demoSessions) {
		t.Fatalf("got %d choices, want %d", len(got.SessionChoices), len(demoSessions))
	}
}

func TestSelectSessionSwitchesAndRejectsUnknown(t *testing.T) {
	h := newHarness(t)
	got := h.SelectSession(context.Background(), "sess-1")
	if got.Err != "" || !strings.Contains(got.Notice, "Cockpit Feature Tour") {
		t.Errorf("select session: got %q/%q", got.Err, got.Notice)
	}
	if h.Title() != "Cockpit Feature Tour" {
		t.Errorf("title not updated: got %q", h.Title())
	}
	if got := h.SelectSession(context.Background(), "nope"); got.Err == "" {
		t.Errorf("unknown session selected silently: %+v", got)
	}
}

func TestRunNewStartsFreshSession(t *testing.T) {
	h := newHarness(t)
	h.mu.Lock()
	h.history = append(h.history, ports.Message{Text: "old message"})
	h.mu.Unlock()

	got := h.Run(context.Background(), "new", "")
	if !got.ClearTranscript {
		t.Errorf("got %+v, want ClearTranscript=true", got)
	}
	if got.Notice == "" {
		t.Errorf("got empty Notice, want non-empty")
	}
	if len(h.History()) != 0 {
		t.Errorf("got history %+v, want empty after /new", h.History())
	}
	if h.Title() != "New Session" {
		t.Errorf("got title %q, want %q", h.Title(), "New Session")
	}
}

func TestRunClearEmptiesHistoryAndAsksToClearTranscript(t *testing.T) {
	h := newHarness(t)
	h.mu.Lock()
	h.history = append(h.history, ports.Message{Text: "leftover"})
	h.mu.Unlock()

	got := h.Run(context.Background(), "clear", "")
	if !got.ClearTranscript {
		t.Errorf("got %+v, want ClearTranscript", got)
	}
	if len(h.History()) != 0 {
		t.Errorf("got history %+v, want empty after /clear", h.History())
	}
}

func TestRunCompactShrinksUsageAndReportsBeforeAfter(t *testing.T) {
	h := newHarness(t)
	before := h.ContextUsage()

	got := h.Run(context.Background(), "compact", "")
	after := h.ContextUsage()

	if after.InputTokens >= before.InputTokens {
		t.Errorf("got InputTokens %d, want fewer than %d after /compact", after.InputTokens, before.InputTokens)
	}
	if !strings.Contains(got.Notice, "Compacted") {
		t.Errorf("got notice %q, want it to say the context was compacted", got.Notice)
	}
}

func TestRunQuit(t *testing.T) {
	h := newHarness(t)
	got := h.Run(context.Background(), "quit", "")
	if !got.Quit {
		t.Errorf("got %+v, want Quit", got)
	}
}

func TestRunUnknownCommandIsAnError(t *testing.T) {
	h := newHarness(t)
	got := h.Run(context.Background(), "bogus", "")
	if got.Err == "" || !strings.Contains(got.Err, "/bogus") {
		t.Errorf("got %+v, want an Err naming /bogus", got)
	}
}

func TestRunUnknownCommandWithArgsIncludesThemInTheError(t *testing.T) {
	h := newHarness(t)
	got := h.Run(context.Background(), "bogus", "some args")
	if !strings.Contains(got.Err, "/bogus some args") {
		t.Errorf("got %+v, want the Err to include the args", got)
	}
}

func TestSelectModelAppliesAKnownChoice(t *testing.T) {
	h := newHarness(t)
	choice := demoModels[len(demoModels)-1]
	got := h.SelectModel(context.Background(), choice)
	if got.Err != "" {
		t.Fatalf("got %+v, want no error", got)
	}
	if !strings.Contains(got.Notice, choice) {
		t.Errorf("got notice %q, want it to name %q", got.Notice, choice)
	}
	if h.Model().Name != choice {
		t.Errorf("got Model().Name %q, want %q", h.Model().Name, choice)
	}
}

func TestSelectModelUnknownChoiceIsAnError(t *testing.T) {
	h := newHarness(t)
	before := h.Model()
	got := h.SelectModel(context.Background(), "not-a-real-model")
	if got.Err == "" {
		t.Error("expected an error for an unknown model name")
	}
	if h.Model() != before {
		t.Errorf("got Model()=%+v, want it unchanged after a rejected selection", h.Model())
	}
}

func TestResolveWithNoRegisteredWaitIsANoOp(t *testing.T) {
	h := newHarness(t)
	h.Resolve("no-such-id", ports.DecisionOnce) // must not panic or block
}

func TestModelDefaultsToFirstDemoModel(t *testing.T) {
	h := newHarness(t)
	if got := h.Model().Name; got != demoModels[0] {
		t.Errorf("got %q, want %q", got, demoModels[0])
	}
}
