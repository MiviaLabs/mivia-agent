package agent

import (
	"context"
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

	EmitCacheUsage(Options{OnEvent: func(event Event) { got = event }, EventBus: bus}, "deepseek", "deepseek-v4-pro",
		provider.CacheUsage{Reported: false, InputTokens: 100, CachedInputTokens: 80})
	if got.Kind == EventCacheUsage || busFired {
		t.Fatalf("unreported usage must not publish, got event=%+v busFired=%v", got, busFired)
	}

	EmitCacheUsage(Options{OnEvent: func(event Event) { got = event }, EventBus: bus}, "deepseek", "deepseek-v4-pro",
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
	EmitCacheUsage(Options{OnEvent: func(event Event) { got = event }}, "deepseek", "deepseek-v4-pro",
		provider.CacheUsage{Reported: true, Style: provider.CacheStyleImplicit, InputTokens: 0, CachedInputTokens: 0})
	if got.Detail != "prompt cache: 0/0 tokens cached (0%)" {
		t.Fatalf("zero-input detail = %q", got.Detail)
	}
}
