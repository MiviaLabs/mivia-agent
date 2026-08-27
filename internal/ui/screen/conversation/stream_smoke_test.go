package conversation

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// TestConversationScreen_StreamingEventDeduplicationSmoke verifies that a realistic
// streaming turn with intermediate notices and usage does not duplicate assistant
// or user text in the transcript or screen view.
func TestConversationScreen_StreamingEventDeduplicationSmoke(t *testing.T) {
	themes, err := theme.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	s := New(themes[0], theme.TierASCII, themes, nil, nil, 80, fixedNow)
	next, _ := s.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	s = next.(Screen)

	at := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	events := []uievent.Event{
		{Kind: uievent.KindTurnStart, Seq: 1, At: at, Body: uievent.TurnStartBody{Input: "check repo health"}},
		{Kind: uievent.KindNotice, Seq: 2, At: at, Body: uievent.NoticeBody{Text: "iteration 1"}},
		{Kind: uievent.KindTextDelta, Seq: 3, At: at, Body: uievent.TextDeltaBody{Text: "Everything is clean and healthy."}},
		{Kind: uievent.KindNotice, Seq: 4, At: at, Body: uievent.NoticeBody{Text: "prompt cache: 2000/2000 tokens (100%)"}},
		{Kind: uievent.KindUsage, Seq: 5, At: at, Body: uievent.UsageBody{InputTokens: 2000, OutputTokens: 50, CachedTokens: 2000, CostUSD: 0.005}},
		{Kind: uievent.KindTextEnd, Seq: 6, At: at, Body: uievent.TextEndBody{Text: "Everything is clean and healthy."}},
		{Kind: uievent.KindTurnEnd, Seq: 7, At: at, Body: uievent.TurnEndBody{Reason: "completed"}},
	}

	for _, ev := range events {
		next, _ = s.Update(uievent.EventMsg{Event: ev})
		s = next.(Screen)
	}

	dump := ansi.Strip(s.transcript.Dump())

	// Assistant response must occur exactly once
	if c := strings.Count(dump, "Everything is clean and healthy."); c != 1 {
		t.Errorf("assistant text occurrence count=%d, want 1 in transcript dump:\n%s", c, dump)
	}

	// User input must occur exactly once
	if c := strings.Count(dump, "check repo health"); c != 1 {
		t.Errorf("user input occurrence count=%d, want 1 in transcript dump:\n%s", c, dump)
	}

	// Notices must occur exactly once
	for _, notice := range []string{"iteration 1", "prompt cache: 2000/2000 tokens (100%)"} {
		if c := strings.Count(dump, notice); c != 1 {
			t.Errorf("notice %q count=%d, want 1 in transcript dump:\n%s", notice, c, dump)
		}
	}

	// Full rendered view must also contain the assistant text
	view := ansi.Strip(s.View())
	if !strings.Contains(view, "Everything is clean and healthy.") {
		t.Errorf("rendered view missing assistant text:\n%s", view)
	}
}

// TestConversationScreen_MultiTurnStreamingSmoke verifies that multiple sequential
// streaming turns render accurately without text leakage between turns.
func TestConversationScreen_MultiTurnStreamingSmoke(t *testing.T) {
	themes, err := theme.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	s := New(themes[0], theme.TierASCII, themes, nil, nil, 80, fixedNow)
	next, _ := s.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	s = next.(Screen)

	at := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)

	// Turn 1
	turn1 := []uievent.Event{
		{Kind: uievent.KindTurnStart, Seq: 1, At: at, Body: uievent.TurnStartBody{Input: "first prompt"}},
		{Kind: uievent.KindTextDelta, Seq: 2, At: at, Body: uievent.TextDeltaBody{Text: "first reply"}},
		{Kind: uievent.KindTextEnd, Seq: 3, At: at, Body: uievent.TextEndBody{Text: "first reply"}},
		{Kind: uievent.KindTurnEnd, Seq: 4, At: at, Body: uievent.TurnEndBody{Reason: "completed"}},
	}
	for _, ev := range turn1 {
		next, _ = s.Update(uievent.EventMsg{Event: ev})
		s = next.(Screen)
	}

	// Turn 2
	turn2 := []uievent.Event{
		{Kind: uievent.KindTurnStart, Seq: 5, At: at, Body: uievent.TurnStartBody{Input: "second prompt"}},
		{Kind: uievent.KindTextDelta, Seq: 6, At: at, Body: uievent.TextDeltaBody{Text: "second reply"}},
		{Kind: uievent.KindTextEnd, Seq: 7, At: at, Body: uievent.TextEndBody{Text: "second reply"}},
		{Kind: uievent.KindTurnEnd, Seq: 8, At: at, Body: uievent.TurnEndBody{Reason: "completed"}},
	}
	for _, ev := range turn2 {
		next, _ = s.Update(uievent.EventMsg{Event: ev})
		s = next.(Screen)
	}

	dump := ansi.Strip(s.transcript.Dump())

	if c := strings.Count(dump, "first prompt"); c != 1 {
		t.Errorf("first prompt count=%d, want 1", c)
	}
	if c := strings.Count(dump, "first reply"); c != 1 {
		t.Errorf("first reply count=%d, want 1", c)
	}
	if c := strings.Count(dump, "second prompt"); c != 1 {
		t.Errorf("second prompt count=%d, want 1", c)
	}
	if c := strings.Count(dump, "second reply"); c != 1 {
		t.Errorf("second reply count=%d, want 1", c)
	}
}
