package cli

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// TestTokenUsageEventReachesBridgeAsCtxTokens is the wiring half of the
// live-context fix: agentEventBridgeCallback must forward EventTokenUsage's
// provider-reported input tokens to the bridge instead of silently dropping
// them (the gap that left the status bar frozen at its turn-start percentage
// until the whole turn finished).
func TestTokenUsageEventReachesBridgeAsCtxTokens(t *testing.T) {
	b := newStreamBridge()
	cb := agentEventBridgeCallback(b)
	typed, err := events.NewTokenUsageEvent("deepseek", "deepseek-chat", 500, 40, 480, 1.0)
	if err != nil {
		t.Fatal(err)
	}
	cb(agent.Event{Kind: agent.EventTokenUsage, TokenUsage: &typed})

	d := b.Drain()
	if !d.CtxTokensSet || d.CtxTokens != 500 {
		t.Fatalf("Drain() = CtxTokens=%d CtxTokensSet=%v, want 500/true", d.CtxTokens, d.CtxTokensSet)
	}
}

func TestLiveContextUsageAppearsInStatusDuringTurn(t *testing.T) {
	m := newReadyChatModel(24, 80)
	// Seed the session so ContextUsage reports a non-zero percentage.
	if err := m.session.SetPromptBudget(1000); err != nil {
		t.Fatal(err)
	}
	m.session.Messages = []provider.Message{
		{Role: provider.RoleUser, Content: strings.Repeat("x", 800)},
	}
	m.messages = []string{"user: hi"}
	m.renderVP()

	m.waiting = true
	m.turnStart = time.Now()
	usage := m.session.ContextUsage()
	if usage.Percent <= 0 {
		t.Fatalf("precondition: usage = %+v, want Percent > 0", usage)
	}
	want := fmt.Sprintf("ctx %d%%", usage.Percent)

	view := m.View()
	plain := stripANSI(view)
	if !strings.Contains(plain, want) {
		t.Fatalf("waiting render missing live context %q:\n%s", want, plain)
	}

	// Idle turns must not show a context percentage.
	m.waiting = false
	plainIdle := stripANSI(m.View())
	if strings.Contains(plainIdle, "ctx ") {
		t.Fatalf("idle render must not show context percentage:\n%s", plainIdle)
	}
}

// TestLiveContextUsagePerStepBeatsStaleSessionMessages guards the bug where
// the status bar's ctx% only moved once, at turn finish, because it read
// session.ContextUsage() (derived from s.Messages, which stays frozen at its
// turn-start value until the turn commits) instead of the per-step token
// count the agent loop already reports via EventTokenUsage. A mid-turn
// updateFromDrain carrying a live CtxTokens sample must override the stale,
// small percentage session.ContextUsage() would otherwise report.
func TestLiveContextUsagePerStepBeatsStaleSessionMessages(t *testing.T) {
	m := newReadyChatModel(24, 80)
	if err := m.session.SetPromptBudget(1000); err != nil {
		t.Fatal(err)
	}
	// A short, turn-start history: ContextUsage() alone would report a small
	// percentage that never changes mid-turn.
	m.session.Messages = []provider.Message{
		{Role: provider.RoleUser, Content: "hi"},
	}
	m.waiting = true

	staleUsage := m.session.ContextUsage()
	if staleUsage.Percent >= 10 {
		t.Fatalf("precondition: stale usage = %+v, want a small percentage", staleUsage)
	}

	// Simulate a step's EventTokenUsage arriving through the bridge: the
	// provider reported 500 input tokens for that step's own request, well
	// above what the stale session history would estimate.
	m.updateFromDrain(bridgeDrain{CtxTokens: 500, CtxTokensSet: true})

	if got := m.liveCtxPercent(); got != 50 {
		t.Fatalf("liveCtxPercent() = %d, want 50 (500/1000 budget) from the live per-step sample", got)
	}
	if got := m.statusDetail(); !strings.Contains(got, "ctx 50%") {
		t.Fatalf("statusDetail() = %q, want it to include the live ctx 50%%", got)
	}
}

func TestStatusDetailAppendsContextOnlyWhileWaiting(t *testing.T) {
	m := newReadyChatModel(24, 80)
	if err := m.session.SetPromptBudget(1000); err != nil {
		t.Fatal(err)
	}
	m.session.Messages = []provider.Message{
		{Role: provider.RoleUser, Content: strings.Repeat("x", 800)},
	}
	m.stepDetail = "searching"

	m.waiting = true
	if got := m.statusDetail(); !strings.HasPrefix(got, "searching") || !strings.Contains(got, "ctx ") {
		t.Fatalf("waiting statusDetail = %q, want stepDetail + ctx", got)
	}

	// Empty stepDetail: the status bar keeps the live ctx usage.
	m.stepDetail = ""
	if got := m.statusDetail(); !strings.Contains(got, "ctx ") {
		t.Fatalf("waiting statusDetail with empty step = %q, want ctx", got)
	}

	m.waiting = false
	m.stepDetail = "searching"
	if got := m.statusDetail(); got != "searching" {
		t.Fatalf("idle statusDetail = %q, want stepDetail unchanged", got)
	}
}
