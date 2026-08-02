package agent

import (
	"context"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

func TestEmitTokenUsagePublishesOnlyWhenReported(t *testing.T) {
	var got Event
	var busEvent events.Event
	var busFired bool
	bus := events.New()
	bus.Subscribe(events.KindTokenUsage, events.HandlerFunc(func(_ context.Context, event events.Event) { busEvent = event; busFired = true }))

	EmitTokenUsage(Options{OnEvent: func(event Event) { got = event }, EventBus: bus}, "deepseek", "deepseek-v4-pro",
		provider.TokenUsage{Reported: false, InputTokens: 100, OutputTokens: 50}, 96, 1.04)
	if got.Kind == EventTokenUsage || busFired {
		t.Fatalf("unreported usage must not publish, got event=%+v busFired=%v", got, busFired)
	}

	EmitTokenUsage(Options{OnEvent: func(event Event) { got = event }, EventBus: bus}, "deepseek", "deepseek-v4-pro",
		provider.TokenUsage{Reported: true, InputTokens: 100, OutputTokens: 50}, 96, 1.04)
	bus.Flush()
	if got.Kind != EventTokenUsage || got.TokenUsage == nil {
		t.Fatalf("typed event = %+v", got)
	}
	if got.TokenUsage.Provider != "deepseek" || got.TokenUsage.InputTokens != 100 || got.TokenUsage.OutputTokens != 50 {
		t.Fatalf("typed event fields = %+v", got.TokenUsage)
	}
	if got.TokenUsage.EstimatedTokens != 96 || got.TokenUsage.CalibrationRatio != 1.04 {
		t.Fatalf("typed event drift fields = %+v", got.TokenUsage)
	}
	if !busFired || busEvent.Kind != events.KindTokenUsage {
		t.Fatalf("bus event not published, got %+v fired=%v", busEvent, busFired)
	}
	if got.Content != "" || got.Input != "" || got.Output != "" {
		t.Fatalf("token usage event carried generic content: %+v", got)
	}
	if busEvent.Content != "" || busEvent.Input != "" || busEvent.Output != "" {
		t.Fatalf("bus event carried content: %+v", busEvent)
	}
}
