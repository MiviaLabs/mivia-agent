package legacytui

import (
	"context"
	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"io"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// compactionTestEvent builds one valid typed compaction record for the TUI
// tests. The counts are the ones renderCompactionNotice reports.
func compactionTestEvent(t *testing.T) events.CompactionEvent {
	t.Helper()
	event, err := events.NewCompactionEvent(events.CompactionEventParams{
		Trigger: "threshold", BeforeTokens: 80, AfterTokens: 40,
		ElidedMessages: 1, ElidedBytes: 4096, SummaryVersion: 1,
		SourceRange: contextstate.SourceRange{
			Start: contextstate.SourceID{SessionID: "session", Sequence: 1},
			End:   contextstate.SourceID{SessionID: "session", Sequence: 3},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return event
}

// TestCompactionEventReachesBridgeAsBanner pins the dispatch, not the
// formatter: an automatic (threshold) compaction emitted during a turn must
// travel agentEventBridgeCallback -> PushCompletedBanner and land in the
// transcript with its elision counts. Only renderCompactionNotice was
// covered before, so deleting the whole `case agent.EventCompaction` arm
// kept the suite green.
func TestCompactionEventReachesBridgeAsBanner(t *testing.T) {
	typed := compactionTestEvent(t)
	bridge := cli.NewStreamBridge()
	agentEventBridgeCallback(bridge)(agent.Event{Kind: agent.EventCompaction, Compaction: &typed})

	drain := bridge.Drain()
	if len(drain.Tools) == 0 {
		t.Fatal("compaction event never reached the bridge")
	}
	var notice string
	for _, evt := range drain.Tools {
		if evt.Name == "context" && evt.Detail != "completed" {
			notice = evt.Detail
		}
	}
	if !strings.Contains(notice, "80 -> 40") {
		t.Fatalf("banner detail = %q, want the token delta", notice)
	}
	if !strings.Contains(notice, "1 tool result elided") {
		t.Fatalf("banner detail = %q, want the elision counts", notice)
	}
}

// TestCompactionEventWithoutPayloadPushesNoBanner pins the nil guard: a
// compaction event that carries no typed record must not open a row.
func TestCompactionEventWithoutPayloadPushesNoBanner(t *testing.T) {
	bridge := cli.NewStreamBridge()
	agentEventBridgeCallback(bridge)(agent.Event{Kind: agent.EventCompaction, Detail: "context compacted"})
	if drain := bridge.Drain(); len(drain.Tools) != 0 {
		t.Fatalf("payload-less compaction pushed %d tool rows, want 0", len(drain.Tools))
	}
}

// TestTuiBridgeReceivesAutomaticCompactionBanner is the TUI's end-to-end
// automatic-compaction proof. It drives a real compacting turn through the
// EXACT callback tui_start_methods.go passes into SendUserWith* -
// agentEventBridgeCallback(bridge) - and requires the compaction banner to
// land in the bridge drain the transcript is rendered from.
//
// This is the TUI counterpart of the --json wire check
// (TestPlainCompactionEmitsTypedEventToTheTurnCallback in internal/cli): the
// session-level callback publication is what lets emitContextCompaction reach
// the bridge at all, and before it a threshold compaction was invisible in
// the TUI exactly as it was on the json wire.
//
// Unlike TestCompactionEventReachesBridgeAsBanner above, which hands the
// callback a synthetic agent.Event, this test drives the callback through a
// real chat.Session turn via SendUserWithEvent, exercising the full path from
// automatic compaction to the bridge.
func TestTuiBridgeReceivesAutomaticCompactionBanner(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	res := summaryWiringResolved(t, true)
	session := chat.NewSession(res, welcomeStubCompleter{})
	store, err := cli.SetupSessionContext(session, t.TempDir(), res)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	bridge := cli.NewStreamBridge()
	onEvent := agentEventBridgeCallback(bridge)

	if _, err := session.SendUserWithEvent(context.Background(), "first "+strings.Repeat("x", 2000), io.Discard, onEvent); err != nil {
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
	if _, err := session.SendUserWithEvent(context.Background(), next, io.Discard, onEvent); err != nil {
		t.Fatal(err)
	}

	var banner string
	for _, evt := range bridge.Drain().Tools {
		if evt.Name == "context" && evt.Detail != "completed" {
			banner = evt.Detail
		}
	}
	if banner == "" {
		t.Fatal("an automatic compaction produced no TUI banner: the transcript would show nothing")
	}
	if !strings.Contains(banner, "->") {
		t.Fatalf("banner = %q, want the before/after token delta", banner)
	}
}

func TestRenderCompactionNoticeOmitsContent(t *testing.T) {
	rangeValue := contextstate.SourceRange{
		Start: contextstate.SourceID{SessionID: "session", Sequence: 1},
		End:   contextstate.SourceID{SessionID: "session", Sequence: 3},
	}
	event, err := events.NewCompactionEvent(events.CompactionEventParams{
		Trigger: "threshold", BeforeTokens: 80, AfterTokens: 40,
		ElidedMessages: 1, ElidedBytes: 4096, SourceRange: rangeValue, SummaryVersion: 1,
		Summarized: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	notice := renderCompactionNotice(event)
	if !strings.Contains(notice, "80 -> 40") || strings.Contains(notice, "summary") {
		t.Fatalf("notice = %q", notice)
	}
	if !strings.Contains(notice, "1 tool result elided") || !strings.Contains(notice, "4096 bytes") {
		t.Fatalf("notice missing elision counts: %q", notice)
	}
	// Zero elision keeps the base form.
	plain, err := events.NewCompactionEvent(events.CompactionEventParams{
		Trigger: "threshold", BeforeTokens: 80, AfterTokens: 40,
		SourceRange: rangeValue, SummaryVersion: 1, Summarized: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	plainNotice := renderCompactionNotice(plain)
	if strings.Contains(plainNotice, "elided") {
		t.Fatalf("zero-elision notice included counts: %q", plainNotice)
	}
	// A summarized compaction must NOT carry the structural-only suffix.
	if strings.Contains(plainNotice, "structural only") {
		t.Fatalf("a summarized compaction claimed structural-only: %q", plainNotice)
	}
}

// TestRenderCompactionNoticeMarksStructuralOnly pins the banner an operator
// sees when a compaction dropped messages without summarizing them. The two
// cases used to render identically, so an instant, LLM-free compaction was
// indistinguishable from a summarized one - the report that started this.
func TestRenderCompactionNoticeMarksStructuralOnly(t *testing.T) {
	event, err := events.NewCompactionEvent(events.CompactionEventParams{
		Trigger: "threshold", BeforeTokens: 25000, AfterTokens: 17000,
		SourceRange: contextstate.SourceRange{
			Start: contextstate.SourceID{SessionID: "session", Sequence: 1},
			End:   contextstate.SourceID{SessionID: "session", Sequence: 3},
		},
		SummaryVersion: 1, Summarized: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	notice := renderCompactionNotice(event)
	if !strings.Contains(notice, "structural only") || !strings.Contains(notice, "no summary") {
		t.Fatalf("notice = %q, want it to say the compaction produced no summary", notice)
	}
}

// TestRenderCompactionNoticeIncludesReason pins that the classified Reason
// rides the automatic in-turn banner, not just the manual /compact path
// (compactStructuralOnlyNotice). Without this the operator sees "structural
// only, no summary" with no way to tell an unwired summarizer from a failed
// summary call.
func TestRenderCompactionNoticeIncludesReason(t *testing.T) {
	event, err := events.NewCompactionEvent(events.CompactionEventParams{
		Trigger: "threshold", BeforeTokens: 25000, AfterTokens: 17000,
		SourceRange: contextstate.SourceRange{
			Start: contextstate.SourceID{SessionID: "session", Sequence: 1},
			End:   contextstate.SourceID{SessionID: "session", Sequence: 3},
		},
		SummaryVersion: 1, Summarized: false,
		Reason: "no summarizer is configured for this session",
	})
	if err != nil {
		t.Fatal(err)
	}
	notice := renderCompactionNotice(event)
	if !strings.Contains(notice, "no summarizer is configured for this session") {
		t.Fatalf("notice = %q, want the classified reason", notice)
	}
}
