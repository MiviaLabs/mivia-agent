package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

func TestEmitCompactionAfterCommitOnly(t *testing.T) {
	principal, err := contextstate.NewPrincipal("workspace", "session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := contextstate.NewBindingRevision("provider", "model", 1)
	if err != nil {
		t.Fatal(err)
	}
	rangeValue := contextstate.SourceRange{
		Start: contextstate.SourceID{SessionID: principal.SessionID, Sequence: 1},
		End:   contextstate.SourceID{SessionID: principal.SessionID, Sequence: 2},
	}
	preparation, err := contextmgr.CapturePreparation(
		contextmgr.PrepareInput{Messages: []provider.Message{{Role: provider.RoleUser, Content: "question"}}, Budget: 100, Principal: principal, Binding: binding},
		contextmgr.CheckpointCandidate{SourceRange: rangeValue},
		[]provider.Message{{Role: provider.RoleUser, Content: "question"}}, true, "compact-test",
	)
	if err != nil {
		t.Fatal(err)
	}
	preparation.BeforeTokens, preparation.AfterTokens = 100, 50
	preparation.ElidedMessages, preparation.ElidedBytes = 2, 8000
	var got Event
	var busEvent events.Event
	bus := events.New()
	bus.Subscribe(events.KindCompaction, events.HandlerFunc(func(_ context.Context, event events.Event) { busEvent = event }))
	EmitCompaction(context.Background(), Options{OnEvent: func(event Event) { got = event }, EventBus: bus}, preparation, true, "")
	bus.Flush()
	if got.Kind != EventCompaction || got.Compaction == nil {
		t.Fatalf("typed event = %+v", got)
	}
	if got.Content != "" || got.Input != "" || got.Output != "" {
		t.Fatalf("compaction event carried generic content: %+v", got)
	}
	if got.Compaction.ElidedMessages != 2 || got.Compaction.ElidedBytes != 8000 {
		t.Fatalf("elision counters missing on event: %+v", got.Compaction)
	}
	if !strings.Contains(got.Detail, "2 tool results elided") {
		t.Fatalf("detail missing elision counts: %q", got.Detail)
	}
	if busEvent.Content != "" || busEvent.Input != "" || busEvent.Output != "" {
		t.Fatalf("bus event carried content: %+v", busEvent)
	}
}

func compactedPreparationForTest(t *testing.T) contextmgr.Preparation {
	t.Helper()
	principal, err := contextstate.NewPrincipal("workspace", "session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := contextstate.NewBindingRevision("provider", "model", 1)
	if err != nil {
		t.Fatal(err)
	}
	rangeValue := contextstate.SourceRange{
		Start: contextstate.SourceID{SessionID: principal.SessionID, Sequence: 1},
		End:   contextstate.SourceID{SessionID: principal.SessionID, Sequence: 2},
	}
	preparation, err := contextmgr.CapturePreparation(
		contextmgr.PrepareInput{Messages: []provider.Message{{Role: provider.RoleUser, Content: "question"}}, Budget: 100, Principal: principal, Binding: binding},
		contextmgr.CheckpointCandidate{SourceRange: rangeValue},
		[]provider.Message{{Role: provider.RoleUser, Content: "question"}}, true, "compact-test",
	)
	if err != nil {
		t.Fatal(err)
	}
	preparation.BeforeTokens, preparation.AfterTokens = 100, 50
	preparation.ElidedMessages, preparation.ElidedBytes = 2, 8000
	return preparation
}

// TestEmitCompactionRecordsToUsageWriter pins that recordUsage is called
// synchronously here, same as EmitTokenUsage/EmitCacheUsage: the record is
// visible to writer as soon as EmitCompaction returns. The non-blocking
// guarantee against a slow/contended write lives in the production
// UsageWriter implementation (storage.usageWriter.Record dispatches its own
// write off this call's goroutine, tracked against the store's own
// WaitGroup) - see internal/storage/usage_events_test.go for that contract's
// own coverage, and TestSQLiteCloseWaitsForInFlightUsageWrites for the
// specific race this design prevents.
func TestEmitCompactionRecordsToUsageWriter(t *testing.T) {
	preparation := compactedPreparationForTest(t)
	writer := &fakeUsageWriter{}
	EmitCompaction(context.Background(), Options{UsageWriter: writer, SessionID: "s1", TurnID: "turn:1"}, preparation, true, "")
	records := writer.recordsCopy()
	if len(records) != 1 {
		t.Fatalf("records = %v, want 1", records)
	}
	rec := records[0]
	if rec.Kind != "compaction" || rec.SessionID != "s1" || rec.TurnID != "turn:1" {
		t.Fatalf("record identity = %+v", rec)
	}
	if rec.BeforeTokens != 100 || rec.AfterTokens != 50 || rec.ElidedMessages != 2 || rec.ElidedBytes != 8000 {
		t.Fatalf("record fields = %+v", rec)
	}
	if rec.Summarized == nil || !*rec.Summarized {
		t.Fatalf("record.Summarized = %v, want a pointer to true", rec.Summarized)
	}
}

func TestEmitCompactionSwallowsWriterError(t *testing.T) {
	preparation := compactedPreparationForTest(t)
	writer := &fakeUsageWriter{err: errors.New("boom")}
	var got Event
	EmitCompaction(context.Background(), Options{OnEvent: func(e Event) { got = e }, UsageWriter: writer}, preparation, true, "")
	if got.Kind != EventCompaction {
		t.Fatalf("event still expected despite writer error, got %+v", got)
	}
}

func TestEmitCompactionNilUsageWriterIsNoop(t *testing.T) {
	preparation := compactedPreparationForTest(t)
	EmitCompaction(context.Background(), Options{}, preparation, true, "")
}
