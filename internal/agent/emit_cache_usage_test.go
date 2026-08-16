package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

func TestEmitCacheUsagePublishesOnlyWhenReported(t *testing.T) {
	var got Event
	var busEvent events.Event
	var busFired bool
	bus := events.New()
	bus.Subscribe(events.KindCacheUsage, events.HandlerFunc(func(_ context.Context, event events.Event) { busEvent = event; busFired = true }))

	EmitCacheUsage(context.Background(), Options{OnEvent: func(event Event) { got = event }, EventBus: bus}, "deepseek", "deepseek-v4-pro",
		provider.CacheUsage{Reported: false, InputTokens: 100, CachedInputTokens: 80})
	if got.Kind == EventCacheUsage || busFired {
		t.Fatalf("unreported usage must not publish, got event=%+v busFired=%v", got, busFired)
	}

	EmitCacheUsage(context.Background(), Options{OnEvent: func(event Event) { got = event }, EventBus: bus}, "deepseek", "deepseek-v4-pro",
		provider.CacheUsage{Reported: true, Style: provider.CacheStyleImplicit, InputTokens: 100, CachedInputTokens: 80, CacheWriteTokens: 0})
	bus.Flush()
	if got.Kind != EventCacheUsage || got.CacheUsage == nil {
		t.Fatalf("typed event = %+v", got)
	}
	if got.CacheUsage.Provider != "deepseek" || got.CacheUsage.CachedInputTokens != 80 {
		t.Fatalf("typed event fields = %+v", got.CacheUsage)
	}
	if !busFired || busEvent.Kind != events.KindCacheUsage {
		t.Fatalf("bus event not published, got %+v fired=%v", busEvent, busFired)
	}
	if got.Content != "" || got.Input != "" || got.Output != "" {
		t.Fatalf("cache usage event carried generic content: %+v", got)
	}
	if busEvent.Content != "" || busEvent.Input != "" || busEvent.Output != "" {
		t.Fatalf("bus event carried content: %+v", busEvent)
	}
	if got.Detail != "prompt cache: 80/100 tokens cached (80%)" {
		t.Fatalf("detail must carry the hit rate percent, got %q", got.Detail)
	}

	// Zero input tokens must not panic and must read as 0%.
	EmitCacheUsage(context.Background(), Options{OnEvent: func(event Event) { got = event }}, "deepseek", "deepseek-v4-pro",
		provider.CacheUsage{Reported: true, Style: provider.CacheStyleImplicit, InputTokens: 0, CachedInputTokens: 0})
	if got.Detail != "prompt cache: 0/0 tokens cached (0%)" {
		t.Fatalf("zero-input detail = %q", got.Detail)
	}
}

func TestEmitCacheUsageRecordsToUsageWriter(t *testing.T) {
	writer := &fakeUsageWriter{}
	EmitCacheUsage(context.Background(), Options{UsageWriter: writer, SessionID: "s1", TurnID: "turn:1"}, "deepseek", "deepseek-v4-pro",
		provider.CacheUsage{Reported: true, Style: provider.CacheStyleImplicit, InputTokens: 100, CachedInputTokens: 80, CacheWriteTokens: 5})
	records := writer.recordsCopy()
	if len(records) != 1 {
		t.Fatalf("records = %v, want 1", records)
	}
	rec := records[0]
	if rec.Kind != "cache_usage" || rec.SessionID != "s1" || rec.TurnID != "turn:1" {
		t.Fatalf("record identity = %+v", rec)
	}
	if rec.InputTokens != 100 || rec.CachedInputTokens != 80 || rec.CacheWriteTokens != 5 {
		t.Fatalf("record fields = %+v", rec)
	}
}

func TestEmitCacheUsageSwallowsWriterError(t *testing.T) {
	writer := &fakeUsageWriter{err: errors.New("boom")}
	var got Event
	EmitCacheUsage(context.Background(), Options{OnEvent: func(e Event) { got = e }, UsageWriter: writer}, "deepseek", "deepseek-v4-pro",
		provider.CacheUsage{Reported: true, Style: provider.CacheStyleImplicit, InputTokens: 100, CachedInputTokens: 80})
	if got.Kind != EventCacheUsage {
		t.Fatalf("event still expected despite writer error, got %+v", got)
	}
}

func TestEmitCacheUsageNilUsageWriterIsNoop(t *testing.T) {
	EmitCacheUsage(context.Background(), Options{}, "deepseek", "deepseek-v4-pro",
		provider.CacheUsage{Reported: true, Style: provider.CacheStyleImplicit, InputTokens: 100, CachedInputTokens: 80})
}
