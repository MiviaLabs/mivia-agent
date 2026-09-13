package uiadapter_test

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/transcript"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
)

// TestRenderSmoke_ReasoningShowsADuration is the offline smoke test ADLC
// mandates for a UI-ship phase (C1). It drives a realistic
// reasoning-then-answer event sequence - no live credentials, no mocked
// block construction - through the real uiadapter.TranslateEvent and the
// real transcript renderer, and asserts the settled reasoning block
// renders its "Thought for" duration line exactly once, never the old
// "N words" header.
func TestRenderSmoke_ReasoningShowsADuration(t *testing.T) {
	seq := []agent.Event{
		{Kind: agent.EventThinking, Content: "weigh the cap against the jitter"},
		{Kind: agent.EventAssistant, Detail: "delta", Content: "I will add a bounded retry."},
	}

	m := transcript.New(loadTestTheme(t), theme.TierASCII)
	m.SetSize(80, 40)
	for _, ev := range seq {
		for _, out := range uiadapter.TranslateEvent(ev) {
			m, _ = m.HandleEvent(out)
		}
	}

	rows, _ := m.ExpandedRows(80)
	full := ansi.Strip(strings.Join(rows, "\n"))
	if c := strings.Count(full, "Thought for"); c != 1 {
		t.Errorf("\"Thought for\" appears %d times, want exactly 1:\n%s", c, full)
	}
	if strings.Contains(full, "words") {
		t.Errorf("settled reasoning must not show the old word-count header:\n%s", full)
	}
}
