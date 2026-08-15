package agent

import (
	"context"
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
	EmitCompaction(Options{OnEvent: func(event Event) { got = event }, EventBus: bus}, preparation, true, "")
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
