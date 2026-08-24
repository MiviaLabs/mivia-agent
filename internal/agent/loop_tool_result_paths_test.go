package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// Defensive seams on the tool-result path. Coverage for the legacy
// executeToolsParallel/buildExecResult construction paths is gone with the
// legacy engine; the SDK path's equivalent dispatcher-shim behavior is
// pinned in sdk_adapter_coverage_test.go and sdk_dispatcher_shim's own
// tests. This file keeps the shared preview/redaction helper coverage.

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
