package agenttools_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/agenttools"
)

// TestInspectPagingConstants pins the workflow_inspect paging budgets
// (plan v3 P2): the default page size for output text and the hard refusal
// ceiling beyond which the tool must refuse to page an artifact at all.
func TestInspectPagingConstants(t *testing.T) {
	if got, want := agenttools.DefaultInspectPageBytes, 64<<10; got != want {
		t.Fatalf("DefaultInspectPageBytes = %d, want %d", got, want)
	}
	if got, want := agenttools.MaxPageableBytes, 8<<20; got != want {
		t.Fatalf("MaxPageableBytes = %d, want %d", got, want)
	}
}

// TestInspectViewPagingRoundTrip verifies the new paging fields survive an
// encoding/json round trip alongside the legacy Output payload.
func TestInspectViewPagingRoundTrip(t *testing.T) {
	in := agenttools.InspectView{
		RunID:             "wfr-page-1",
		Step:              "build",
		Attempt:           2,
		Status:            "succeeded",
		CoordinatorRunID:  "coord-1",
		TaskID:            "task-1",
		Output:            map[string]any{"verdict": "ok"},
		OutputRef:         "sha256:out",
		OutputDigest:      "outdigest",
		EvidenceSelection: []any{"task"},
		Transition: &agenttools.TransitionView{
			Index: 0, ToStep: "deploy", MatchDigest: "md",
			Selected: map[string]any{"x": "y"},
		},
		OutputText:       "page two of the output text",
		OutputBytes:      200_000,
		OutputOffset:     64 << 10,
		OutputNextOffset: 128 << 10,
	}

	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	raw := string(b)
	for _, key := range []string{"output_text", "output_bytes", "output_offset", "output_next_offset", "output"} {
		if !strings.Contains(raw, `"`+key+`"`) {
			t.Fatalf("marshaled JSON missing %q: %s", key, raw)
		}
	}

	var out agenttools.InspectView
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.OutputText != in.OutputText {
		t.Fatalf("OutputText = %q, want %q", out.OutputText, in.OutputText)
	}
	if out.OutputBytes != in.OutputBytes {
		t.Fatalf("OutputBytes = %d, want %d", out.OutputBytes, in.OutputBytes)
	}
	if out.OutputOffset != in.OutputOffset {
		t.Fatalf("OutputOffset = %d, want %d", out.OutputOffset, in.OutputOffset)
	}
	if out.OutputNextOffset != in.OutputNextOffset {
		t.Fatalf("OutputNextOffset = %d, want %d", out.OutputNextOffset, in.OutputNextOffset)
	}
	if !reflect.DeepEqual(out.Output, in.Output) {
		t.Fatalf("Output = %#v, want %#v", out.Output, in.Output)
	}
	if out.RunID != in.RunID || out.Step != in.Step || out.Attempt != in.Attempt || out.Status != in.Status {
		t.Fatalf("base view fields lost: %+v", out)
	}
	if out.Transition == nil || out.Transition.ToStep != "deploy" || !reflect.DeepEqual(out.Transition.Selected, in.Transition.Selected) {
		t.Fatalf("transition lost: %+v", out.Transition)
	}
}

// TestEmptyInspectViewOmitsPagingFields pins the omitempty contract: an
// empty page (all-zero paging fields, including an exhausted
// OutputNextOffset of 0) must marshal without any of the new keys.
func TestEmptyInspectViewOmitsPagingFields(t *testing.T) {
	in := agenttools.InspectView{RunID: "wfr-page-1", OutputText: "", OutputBytes: 0, OutputOffset: 0, OutputNextOffset: 0}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	raw := string(b)
	for _, key := range []string{"output_text", "output_bytes", "output_offset", "output_next_offset"} {
		if strings.Contains(raw, `"`+key+`"`) {
			t.Fatalf("empty page marshaled forbidden key %q: %s", key, raw)
		}
	}
	if !strings.Contains(raw, `"run_id"`) {
		t.Fatalf("run_id missing from marshaled view: %s", raw)
	}
	// Existing omitempty fields stay absent too, so the page is minimal.
	for _, key := range []string{"output", "transition", "evidence_selection"} {
		if strings.Contains(raw, `"`+key+`"`) {
			t.Fatalf("empty page unexpectedly included %q: %s", key, raw)
		}
	}
}
