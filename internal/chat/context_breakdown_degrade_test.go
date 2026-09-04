package chat

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// unmarshalableToolSpec is a tool schema that cannot be encoded: a channel
// value has no JSON form. It stands in for any spec the pricing step
// cannot read - the same failure EstimatePromptCost reports for the same
// input, so the gauge and the breakdown fail on exactly the same specs.
func unmarshalableToolSpec() provider.ToolSpec {
	return provider.ToolSpec{
		"type":        "function",
		"function":    map[string]any{"name": "broken_tool"},
		"unencodable": make(chan int),
	}
}

// TestBreakdownFailsOnAnUnpriceableToolSchema pins that a schema the
// estimator cannot price is an error, not a silently-zero bucket. A spec
// charged as free would make every displayed bucket understate the window
// while the totals still looked self-consistent.
func TestBreakdownFailsOnAnUnpriceableToolSchema(t *testing.T) {
	tools := append(breakdownTools(), unmarshalableToolSpec())
	b, err := breakdown(breakdownMessages(), tools, breakdownExternal(), provider.ContextAccountingProfile{})
	if err == nil {
		t.Fatal("an unpriceable tool schema was charged instead of reported")
	}
	if !strings.Contains(err.Error(), "marshal tool schema") {
		t.Fatalf("error %q does not name the schema-marshal failure", err)
	}
	if b != (ContextBreakdown{}) {
		t.Fatalf("breakdown returned partial buckets %+v alongside its error; want the zero value", b)
	}
}

// TestCalibratedBreakdownDegradesToCountsOnly pins the display contract on
// that failure: the gauge still has a used-token total to draw, so the
// breakdown must fall back to the tool counts rather than take the whole
// section down. Every token bucket must be zero - a partial bucket here
// would be read as a real measurement.
func TestCalibratedBreakdownDegradesToCountsOnly(t *testing.T) {
	tools := append(breakdownTools(), unmarshalableToolSpec())
	external := breakdownExternal()

	got := calibratedBreakdown(breakdownMessages(), tools, external, provider.ContextAccountingProfile{}, 5000)

	want := ContextBreakdown{
		ToolCount:         len(tools) - len(external),
		ExternalToolCount: len(external),
	}
	if got != want {
		t.Fatalf("calibratedBreakdown = %+v; want counts only %+v", got, want)
	}
	if got.Total() != 0 {
		t.Fatalf("degraded breakdown reports %d tokens; want 0 so no bucket reads as measured", got.Total())
	}
}

// TestAnUnreadableToolSpecIsChargedAsCompiledIn pins toolSpecName's
// fallback. A spec whose shape the name lookup does not recognise yields
// "", which no server tool is keyed by, so it must land in the
// compiled-in bucket. Attributing it to a server would send the operator
// looking for an MCP server that never supplied it.
func TestAnUnreadableToolSpecIsChargedAsCompiledIn(t *testing.T) {
	unreadable := []provider.ToolSpec{
		{"type": "function"},                    // no function object at all
		{"function": "read_file"},               // function is not an object
		{"function": map[string]any{"name": 7}}, // name is not a string
	}
	for _, spec := range unreadable {
		if got := toolSpecName(spec); got != "" {
			t.Fatalf("toolSpecName(%v) = %q; want \"\" so it cannot key a server", spec, got)
		}
	}

	external := map[string]string{"mcp__linear__issue": "linear"}
	b, err := breakdown(nil, unreadable, external, provider.ContextAccountingProfile{})
	if err != nil {
		t.Fatalf("breakdown: %v", err)
	}
	if b.ExternalToolCount != 0 || b.ExternalSchemas != 0 {
		t.Fatalf("unreadable specs were attributed to a server: %+v", b)
	}
	if b.ToolCount != len(unreadable) {
		t.Fatalf("ToolCount = %d; want all %d unreadable specs charged as compiled-in", b.ToolCount, len(unreadable))
	}
	if b.ToolSchemas <= 0 {
		t.Fatal("unreadable specs were charged nothing; they still occupy the window")
	}
}
