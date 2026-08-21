package legacytui

import (
	"fmt"
	"github.com/MiviaLabs/mivia-agent/internal/cli"
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
	b := cli.NewStreamBridge()
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
	plain := cli.StripANSI(view)
	if !strings.Contains(plain, want) {
		t.Fatalf("waiting render missing live context %q:\n%s", want, plain)
	}

	// Idle turns keep showing the context percentage too - it must not
	// disappear the moment a turn finishes.
	m.waiting = false
	plainIdle := cli.StripANSI(m.View())
	if !strings.Contains(plainIdle, "ctx ") {
		t.Fatalf("idle render must still show context percentage:\n%s", plainIdle)
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
	m.updateFromDrain(cli.BridgeDrain{CtxTokens: 500, CtxTokensSet: true})

	if got := m.liveCtxPercent(); got != 50 {
		t.Fatalf("liveCtxPercent() = %d, want 50 (500/1000 budget) from the live per-step sample", got)
	}
	if got := m.statusDetail(); !strings.Contains(got, "ctx 50%") {
		t.Fatalf("statusDetail() = %q, want it to include the live ctx 50%%", got)
	}
}

// TestLiveContextUsageSurvivesQuietStretchBetweenSteps guards the regression
// where a live sample would render correctly once and then revert to the
// stale pre-turn percentage: liveCtxPercent()'s own 500ms-throttle fallback
// unconditionally recomputed session.ContextUsage() (stale, since s.Messages
// is not updated until turn finish) whenever more than 500ms had passed since
// the last update - including a live push - clobbering a fresher live sample
// with a worse one during any tool call that runs longer than 500ms. Once a
// live sample lands this turn, it must win for the rest of the turn
// regardless of how much later liveCtxPercent() is polled.
func TestLiveContextUsageSurvivesQuietStretchBetweenSteps(t *testing.T) {
	m := newReadyChatModel(24, 80)
	if err := m.session.SetPromptBudget(1000); err != nil {
		t.Fatal(err)
	}
	// A short, turn-start history: ContextUsage() alone reports near 0%.
	m.session.Messages = []provider.Message{
		{Role: provider.RoleUser, Content: "hi"},
	}
	m.waiting = true

	m.updateFromDrain(cli.BridgeDrain{CtxTokens: 500, CtxTokensSet: true})
	if got := m.liveCtxPercent(); got != 50 {
		t.Fatalf("liveCtxPercent() right after the push = %d, want 50", got)
	}

	// Simulate a long-running tool call: no new EventTokenUsage arrives, and
	// enough wall-clock time passes that the old throttle window would have
	// expired and recomputed from the stale session history.
	m.cachedCtxPercentAt = time.Now().Add(-time.Second)

	if got := m.liveCtxPercent(); got != 50 {
		t.Fatalf("liveCtxPercent() after a quiet stretch = %d, want the live sample (50) to still win, not the stale pre-turn estimate", got)
	}
}

// TestStatusDetailAlwaysShowsContext pins that the ctx% suffix is not
// turn-scoped chrome: it appears whether the model is mid-turn (with or
// without other step detail text) or idle, so the number a user relies on
// to judge when to /compact never disappears just because the turn ended.
func TestStatusDetailAlwaysShowsContext(t *testing.T) {
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

	// Idle: stepDetail is cleared by the turn-finish path, but ctx% must
	// still render on its own.
	m.waiting = false
	m.stepDetail = ""
	if got := m.statusDetail(); !strings.Contains(got, "ctx ") {
		t.Fatalf("idle statusDetail = %q, want it to still show ctx", got)
	}
}
