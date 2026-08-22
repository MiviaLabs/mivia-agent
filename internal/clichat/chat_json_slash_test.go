package clichat

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
)

// TestJSONSlashSinkInfoAndError pins the generic fallback shape used by every
// slash command that has no typed event above.
func TestJSONSlashSinkInfoAndError(t *testing.T) {
	var buf bytes.Buffer
	sink := &JSONSlashSink{w: &buf}
	sink.Info("current model=foo")
	sink.Error("boom")

	lines := splitNonEmptyLines(buf.String())
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2: %q", len(lines), buf.String())
	}
	var info ndjsonEvent
	if err := json.Unmarshal([]byte(lines[0]), &info); err != nil {
		t.Fatalf("decode info: %v", err)
	}
	if info.Type != "slash_info" || info.Message != "current model=foo" {
		t.Fatalf("info event = %+v", info)
	}
	var errEv ndjsonEvent
	if err := json.Unmarshal([]byte(lines[1]), &errEv); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if errEv.Type != "slash_error" || errEv.Message != "boom" {
		t.Fatalf("error event = %+v", errEv)
	}
}

// TestJSONSlashSinkModelChanged pins the model_changed wire shape, including
// the discarded_effort field only appearing when the switch actually dropped
// an active reasoning effort.
func TestJSONSlashSinkModelChanged(t *testing.T) {
	var buf bytes.Buffer
	sink := &JSONSlashSink{w: &buf}
	sink.ModelChanged("openai", "gpt-4o-mini", reasoning.Level("high"))

	var ev ndjsonEvent
	if err := json.Unmarshal(buf.Bytes(), &ev); err != nil {
		t.Fatalf("decode: %v\nraw: %s", err, buf.String())
	}
	if ev.Type != "model_changed" || ev.Provider != "openai" || ev.Model != "gpt-4o-mini" || ev.DiscardedEffort != "high" {
		t.Fatalf("model_changed event = %+v", ev)
	}

	buf.Reset()
	sink.ModelChanged("openai", "gpt-4o-mini", reasoning.Level(""))
	var ev2 ndjsonEvent
	if err := json.Unmarshal(buf.Bytes(), &ev2); err != nil {
		t.Fatalf("decode: %v\nraw: %s", err, buf.String())
	}
	if ev2.DiscardedEffort != "" {
		t.Fatalf("expected no discarded_effort when nothing was active, got %+v", ev2)
	}
}

// TestJSONSlashSinkEffortChanged pins the effort_changed wire shape.
func TestJSONSlashSinkEffortChanged(t *testing.T) {
	var buf bytes.Buffer
	sink := &JSONSlashSink{w: &buf}
	sink.EffortChanged("gpt-4o-mini", reasoning.Level("medium"))

	var ev ndjsonEvent
	if err := json.Unmarshal(buf.Bytes(), &ev); err != nil {
		t.Fatalf("decode: %v\nraw: %s", err, buf.String())
	}
	if ev.Type != "effort_changed" || ev.Model != "gpt-4o-mini" || ev.Effort != "medium" {
		t.Fatalf("effort_changed event = %+v", ev)
	}
}

// TestSlashSinkForFallsBackToTerminalSinkOutsideJSONMode pins the safety
// property activeJSONSlashSink's doc comment relies on: outside a --json
// replLineMode run, slashSinkFor must never route through the JSON sink, even
// if some other test in this package left activeJSONSlashSink set.
func TestSlashSinkForFallsBackToTerminalSinkOutsideJSONMode(t *testing.T) {
	prev := activeJSONSlashSink
	activeJSONSlashSink = nil
	defer func() { activeJSONSlashSink = prev }()

	sink := SlashSinkFor(nil)
	if _, ok := sink.(terminalSlashSink); !ok {
		t.Fatalf("slashSinkFor(nil) with activeJSONSlashSink=nil = %T, want terminalSlashSink", sink)
	}
}

// TestSlashSinkForUsesActiveJSONSink pins the --json activation path
// replLineMode drives: once activeJSONSlashSink is set, slashSinkFor must
// return it regardless of the *Terminal argument.
func TestSlashSinkForUsesActiveJSONSink(t *testing.T) {
	prev := activeJSONSlashSink
	want := &JSONSlashSink{w: &bytes.Buffer{}}
	activeJSONSlashSink = want
	defer func() { activeJSONSlashSink = prev }()

	got := SlashSinkFor(nil)
	if got != slashSink(want) {
		t.Fatalf("slashSinkFor did not return the active JSON sink: got %T", got)
	}
}
