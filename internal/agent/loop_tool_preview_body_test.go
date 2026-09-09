package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// bigTool returns a result far above the operator preview cap, so a test
// can tell the preview from the body.
type bigTool struct{}

func (bigTool) Name() string               { return "big" }
func (bigTool) Description() string        { return "big" }
func (bigTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (bigTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return strings.Repeat("r", 4*defaultToolPreviewMaxBytes), nil
}

// The operator preview stays what it was - bounded, for the NDJSON stream
// and the log - and the same event now also carries the redacted body
// unbounded, for a consumer that records its own truncation (chat-sync).
func TestBridgeEvents_ToolEventsCarryTheBodyBesideThePreview(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(bigTool{})
	args := `{"content":"` + strings.Repeat("a", 1000) + `"}`
	evs := runSDKToolTurn(t, reg, []provider.ToolCall{tc("call_1", "big", args)}, nil)

	starts := toolEventsOf(evs, EventToolStart)
	if len(starts) == 0 {
		t.Fatal("no tool_start")
	}
	queued := starts[0]
	if len(queued.Input) != 256 {
		t.Fatalf("tool_start Input preview = %d bytes, want the 256-byte cap", len(queued.Input))
	}
	if queued.InputBody != args {
		t.Fatalf("tool_start InputBody = %d bytes, want the whole %d-byte arguments", len(queued.InputBody), len(args))
	}

	ends := toolEventsOf(evs, EventToolEnd)
	if len(ends) != 1 {
		t.Fatalf("tool_end count = %d, want 1", len(ends))
	}
	end := ends[0]
	if len(end.Output) != defaultToolPreviewMaxBytes {
		t.Fatalf("tool_end Output preview = %d bytes, want the %d-byte cap", len(end.Output), defaultToolPreviewMaxBytes)
	}
	if len(end.OutputBody) != 4*defaultToolPreviewMaxBytes {
		t.Fatalf("tool_end OutputBody = %d bytes, want the whole result", len(end.OutputBody))
	}
}

// An ephemeral tool's marker replaces the body on the operator surface; the
// wider field must not reopen that door.
func TestToolEndPreviewOverrideAlsoReplacesTheBody(t *testing.T) {
	ev := toolEndEventFor(toolCallOutcome{id: "call_1", name: "read_skill_resource", body: "the resource body", previewOverride: "[resource elided]"})
	if ev.OutputBody != "[resource elided]" || ev.Output != "[resource elided]" {
		t.Fatalf("tool_end = Output %q OutputBody %q, want the marker in both", ev.Output, ev.OutputBody)
	}
}

// The bus copy carries the bodies too: chat-sync subscribes to the bus, not
// to OnEvent.
func TestEmitCarriesToolBodiesOntoTheBus(t *testing.T) {
	bus := events.New()
	defer bus.Close()
	var got events.Event
	bus.Subscribe(events.KindToolEnd, events.HandlerFunc(func(_ context.Context, ev events.Event) {
		got = ev
	}))
	emit(Options{EventBus: bus}, Event{Kind: EventToolEnd, ToolCallID: "c", Name: "n", Output: "prev", OutputBody: "prev and the rest", InputBody: "in"})
	bus.Flush()
	if got.OutputBody != "prev and the rest" || got.InputBody != "in" {
		t.Fatalf("bus event = %+v, want the bodies carried", got)
	}
}
