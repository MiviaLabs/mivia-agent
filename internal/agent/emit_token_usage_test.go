package agent

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/usage"
)

// fakeUsageWriter records every UsageRecord passed to it, optionally failing
// the call - shared by the three Emit* test files (same package) to test
// recordUsage's wiring and its swallow-the-error contract. All three Emit*
// functions call recordUsage (and therefore Record) synchronously; the
// mutex is defensive rather than load-bearing.
type fakeUsageWriter struct {
	mu      sync.Mutex
	records []usage.UsageRecord
	err     error
}

func (w *fakeUsageWriter) Record(_ context.Context, record usage.UsageRecord) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.records = append(w.records, record)
	return w.err
}

func (w *fakeUsageWriter) recordsCopy() []usage.UsageRecord {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]usage.UsageRecord(nil), w.records...)
}

func TestEmitTokenUsagePublishesOnlyWhenReported(t *testing.T) {
	var got Event
	var busEvent events.Event
	var busFired bool
	bus := events.New()
	bus.Subscribe(events.KindTokenUsage, events.HandlerFunc(func(_ context.Context, event events.Event) { busEvent = event; busFired = true }))

	EmitTokenUsage(context.Background(), Options{OnEvent: func(event Event) { got = event }, EventBus: bus}, "deepseek", "deepseek-v4-pro",
		provider.TokenUsage{Reported: false, InputTokens: 100, OutputTokens: 50}, 96, 1.04)
	if got.Kind == EventTokenUsage || busFired {
		t.Fatalf("unreported usage must not publish, got event=%+v busFired=%v", got, busFired)
	}

	EmitTokenUsage(context.Background(), Options{OnEvent: func(event Event) { got = event }, EventBus: bus}, "deepseek", "deepseek-v4-pro",
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

func TestEmitTokenUsageZeroEstimate(t *testing.T) {
	var got Event
	EmitTokenUsage(context.Background(), Options{OnEvent: func(event Event) { got = event }}, "deepseek", "deepseek-v4-pro",
		provider.TokenUsage{Reported: true, InputTokens: 100, OutputTokens: 50}, 0, 1.0)
	if got.Kind != EventTokenUsage {
		t.Fatalf("expected EventTokenUsage, got %v", got.Kind)
	}
	// With estimatedTokens=0, the drift string uses the "actual N in / M out" form
	if got.Detail == "" {
		t.Fatal("expected non-empty detail with zero estimate")
	}
}

func TestEmitTokenUsageWithEventIdentity(t *testing.T) {
	var got Event
	bus := events.New()
	var busGot *events.Event
	bus.Subscribe(events.KindTokenUsage, events.HandlerFunc(func(_ context.Context, ev events.Event) {
		cp := ev
		busGot = &cp
	}))
	identity := &events.Identity{DefinitionName: "test", DefinitionSource: "test", InstanceID: "inst-1", ModelGeneration: 1}
	EmitTokenUsage(context.Background(), Options{
		OnEvent:       func(event Event) { got = event },
		EventIdentity: identity,
		EventBus:      bus,
		SessionID:     "sess-1",
		TurnID:        "turn-1",
	}, "deepseek", "deepseek-v4-pro",
		provider.TokenUsage{Reported: true, InputTokens: 100, OutputTokens: 50}, 96, 1.0)
	if got.Kind != EventTokenUsage {
		t.Fatalf("expected EventTokenUsage, got %v", got.Kind)
	}
	bus.Flush()
	if busGot == nil {
		t.Fatal("expected event bus publish")
	}
	if busGot.Identity == nil || busGot.Identity.DefinitionName != "test" {
		t.Fatal("expected identity in bus event")
	}
}

func TestEmitTokenUsageErrorPath(t *testing.T) {
	var got Event
	EmitTokenUsage(context.Background(), Options{
		OnEvent: func(event Event) { got = event },
	}, "", "",
		provider.TokenUsage{Reported: true, InputTokens: 100, OutputTokens: 50}, 96, 1.0)
	if got.Kind != "" {
		t.Fatalf("expected no event for empty provider/model, got %v", got.Kind)
	}
}

func TestEmitTokenUsageRecordsToUsageWriter(t *testing.T) {
	writer := &fakeUsageWriter{}
	EmitTokenUsage(context.Background(), Options{UsageWriter: writer, SessionID: "s1", TurnID: "turn:1"}, "deepseek", "deepseek-v4-pro",
		provider.TokenUsage{Reported: true, InputTokens: 100, OutputTokens: 50}, 96, 1.04)
	records := writer.recordsCopy()
	if len(records) != 1 {
		t.Fatalf("records = %v, want 1", records)
	}
	rec := records[0]
	if rec.Kind != "token_usage" || rec.SessionID != "s1" || rec.TurnID != "turn:1" {
		t.Fatalf("record identity = %+v", rec)
	}
	if rec.Provider != "deepseek" || rec.Model != "deepseek-v4-pro" {
		t.Fatalf("record provider/model = %+v", rec)
	}
	if rec.InputTokens != 100 || rec.OutputTokens != 50 || rec.EstimatedTokens != 96 || rec.CalibrationRatio != 1.04 {
		t.Fatalf("record fields = %+v", rec)
	}
}

func TestEmitTokenUsageSwallowsWriterError(t *testing.T) {
	writer := &fakeUsageWriter{err: errors.New("boom")}
	var got Event
	// Must not panic and must still publish the event/OnEvent callback - a
	// durable-write failure never blocks or fails the turn it describes.
	EmitTokenUsage(context.Background(), Options{OnEvent: func(e Event) { got = e }, UsageWriter: writer}, "deepseek", "deepseek-v4-pro",
		provider.TokenUsage{Reported: true, InputTokens: 100, OutputTokens: 50}, 96, 1.04)
	if got.Kind != EventTokenUsage {
		t.Fatalf("event still expected despite writer error, got %+v", got)
	}
}

func TestEmitTokenUsageNilUsageWriterIsNoop(t *testing.T) {
	// Must not panic when UsageWriter is unset - the default everywhere this
	// slice isn't wired.
	EmitTokenUsage(context.Background(), Options{}, "deepseek", "deepseek-v4-pro",
		provider.TokenUsage{Reported: true, InputTokens: 100, OutputTokens: 50}, 96, 1.04)
}
