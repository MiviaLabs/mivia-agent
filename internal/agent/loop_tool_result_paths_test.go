package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// Defensive seams on the tool-result path: the places a batch can go wrong
// before any result body exists. Each must still leave one result per call -
// a missing result orphans a tool_call_id and makes the next provider request
// malformed.

// A dispatcher that cannot be built fails every call in the batch rather than
// returning short: the loop builds this fallback itself when no dispatcher was
// injected, so its failure is the whole batch's failure.
func TestExecuteToolsParallelFailsEveryCallWhenTheDispatcherCannotBeBuilt(t *testing.T) {
	calls := []provider.ToolCall{
		tc("call_a", "read_file", `{"path":"a"}`),
		tc("call_b", "read_file", `{"path":"b"}`),
	}
	var ended []string
	// A nil registry is the one construction failure NewToolDispatcher
	// reports; the loop's own Run guards against it, so only this direct call
	// can reach the branch.
	results := executeToolsParallel(context.Background(), calls, nil, Options{
		OnEvent: func(e Event) {
			if e.Kind == EventToolEnd {
				ended = append(ended, e.ToolCallID)
			}
		},
	})

	if len(results) != len(calls) {
		t.Fatalf("got %d results for %d calls", len(results), len(calls))
	}
	for i, r := range results {
		if r.err == nil {
			t.Fatalf("result %d reports success although no dispatcher exists", i)
		}
		if !strings.Contains(r.result, "tool registry") {
			t.Fatalf("result %d body %q does not explain the failure", i, r.result)
		}
		// The body must be charged like any other: a result whose parts are
		// empty is charged as zero bytes and emitted as an empty message.
		if r.parts.cappedBody != r.result || r.parts.totalN != len(r.result) {
			t.Fatalf("result %d carries no structured parts: %+v", i, r.parts)
		}
	}
	if len(ended) != len(calls) {
		t.Fatalf("emitted %d tool_end events, want %d", len(ended), len(calls))
	}
}

// A tool that fails without saying anything gets a synthesized body; one that
// failed but still spoke keeps its own words. The model needs the tool's
// account of the failure whenever there is one.
func TestBuildExecResultSynthesizesABodyOnlyWhenTheToolSaidNothing(t *testing.T) {
	reg := tools.NewRegistry()
	task := &toolTask{call: tc("call_x", "some_tool", `{}`)}

	silent := buildExecResult(0, task, reg, Options{}, runtime.Result{
		Err: errors.New("boom"),
	})
	if !strings.Contains(silent.result, "error: boom") {
		t.Fatalf("silent failure body = %q, want the synthesized error", silent.result)
	}
	if silent.parts.cappedBody != silent.result || silent.parts.totalN != len(silent.result) {
		t.Fatalf("synthesized body was not charged: %+v", silent.parts)
	}

	spoke := buildExecResult(0, task, reg, Options{}, runtime.Result{
		Output: []byte("exit=1\nreal tool output"),
		Err:    errors.New("boom"),
	})
	if strings.Contains(spoke.result, "error: boom") {
		t.Fatalf("a failing tool's own output was replaced: %q", spoke.result)
	}
	if spoke.err == nil {
		t.Fatal("the failure itself was dropped")
	}
}

// The argument preview walks arrays as well as objects, and stops descending
// at the depth bound: crafted input must not be able to recurse the previewer
// into a stack overflow.
func TestElideContentPreviewsWalksArraysAndStopsAtMaxDepth(t *testing.T) {
	nested := []any{
		map[string]any{"content": "abcdef"},
		[]any{map[string]any{"content": "xy"}},
		"plain",
	}
	got, ok := elideContentPreviews(nested, 0).([]any)
	if !ok {
		t.Fatalf("array input came back as %T", got)
	}
	first := got[0].(map[string]any)
	if first["content"] != "[content 6 bytes]" {
		t.Fatalf("content inside an array was not elided: %v", first["content"])
	}
	inner := got[1].([]any)[0].(map[string]any)
	if inner["content"] != "[content 2 bytes]" {
		t.Fatalf("content nested two levels deep was not elided: %v", inner["content"])
	}

	// Past the bound the value is returned untouched rather than descended.
	deep := map[string]any{"content": "abcdef"}
	returned := elideContentPreviews(deep, redactJSONMaxDepth+1).(map[string]any)
	if returned["content"] != "abcdef" {
		t.Fatalf("the depth bound did not stop the walk: %v", returned["content"])
	}
}

// The opt-in preview keeps producing valid JSON for realistic tool arguments,
// which is the reason its marshal guard has never been observed to fire.
func TestRedactToolInputOptInPreviewStaysValidJSON(t *testing.T) {
	tools.SetRedactToolArgs(true)
	t.Cleanup(func() { tools.SetRedactToolArgs(false) })

	preview := redactToolInput(`{"path":"x.txt","content":"hello","opts":[1,2,{"content":"deep"}]}`)
	var decoded any
	if err := json.Unmarshal([]byte(preview), &decoded); err != nil {
		t.Fatalf("preview %q is not valid JSON: %v", preview, err)
	}
	if !strings.Contains(preview, "[content 5 bytes]") {
		t.Fatalf("preview did not elide the file body: %q", preview)
	}
}

// The encode guard degrades to a scrubbed preview rather than emitting a
// broken one.
// Production input cannot reach it - json.Unmarshal never yields an
// unencodable value - so the only way to prove the guard works is to hand it
// one directly.
func TestEncodeRedactedPreviewFallsBackForUnencodableValues(t *testing.T) {
	if got := encodeRedactedPreview(make(chan int), `{"path":"x.txt"}`); got != `{"path":"x.txt"}` {
		t.Fatalf("unencodable value produced %q, want the scrubbed raw text", got)
	}
	encoded := encodeRedactedPreview(map[string]any{"path": "x.txt"}, "")
	if !strings.Contains(encoded, `"path":"x.txt"`) {
		t.Fatalf("encoded preview lost its arguments: %q", encoded)
	}
}
