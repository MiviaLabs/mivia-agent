package ledger_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/vcs"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// TestInvocationRunIDFitsWorktreeNameLimit pins that every invocation run ID
// yields a worktree name that vcs.SanitizeName accepts. The digest is truncated
// to 16 bytes, so "workflow-"+runID stays within vcs.MaxWorktreeNameLen. A full
// digest makes the name 81 chars, and SanitizeName rejects it with
// "name is too long" (that rejection contract is pinned by vcs tests).
func TestInvocationRunIDFitsWorktreeNameLimit(t *testing.T) {
	for _, k := range []string{"", "request-1", strings.Repeat("x", 4096)} {
		runID := ledger.InvocationRunID(k)
		if got, want := len(runID), len("wfr-inv-")+32; got != want {
			t.Fatalf("InvocationRunID(%q) length = %d, want %d", k, got, want)
		}
		name, err := vcs.SanitizeName("workflow-" + runID)
		if err != nil {
			t.Fatalf("SanitizeName(\"workflow-\"+InvocationRunID(%q)): %v", k, err)
		}
		if want := "workflow-" + runID; name != want {
			t.Fatalf("SanitizeName = %q, want %q", name, want)
		}
	}
}

// TestInspectPagingConstants pins the workflow_inspect paging budgets
// (plan v3 P2): the default page size for output text and the hard refusal
// ceiling beyond which the tool must refuse to page an artifact at all.
func TestInspectPagingConstants(t *testing.T) {
	if got, want := ledger.DefaultInspectPageBytes, 64<<10; got != want {
		t.Fatalf("DefaultInspectPageBytes = %d, want %d", got, want)
	}
	if got, want := ledger.MaxPageableBytes, 8<<20; got != want {
		t.Fatalf("MaxPageableBytes = %d, want %d", got, want)
	}
}

// TestInspectViewPagingRoundTrip verifies the new paging fields survive an
// encoding/json round trip alongside the legacy Output payload.
func TestInspectViewPagingRoundTrip(t *testing.T) {
	in := ledger.InspectView{
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
		Transition: &ledger.TransitionView{
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

	var out ledger.InspectView
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
	in := ledger.InspectView{RunID: "wfr-page-1", OutputText: "", OutputBytes: 0, OutputOffset: 0, OutputNextOffset: 0}
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
