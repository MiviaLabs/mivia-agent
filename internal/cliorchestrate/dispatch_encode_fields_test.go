package cliorchestrate

// Focused unit tests for the envelope field encoders that decide what the
// parent model sees from a subagent run: setOutputFields (inline vs
// synopsis+output_ref), setErrorFields (error inline vs error_ref, the
// schema-violation never-inline rule, the failed-status fallback), and
// EncodeOneDispatchResult's status defaulting. These paths previously rode
// only indirectly through encodeResults tests.

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

func TestSetOutputFieldsInlineThreshold(t *testing.T) {
	body := []byte(`"hello world"`)
	big := []byte(`"` + strings.Repeat("x", 100) + `"`)

	cases := []struct {
		name       string
		output     []byte
		ref        string
		threshold  int
		wantInline bool
		wantRef    string
	}{
		{name: "below threshold no ref inlines", output: body, ref: "", threshold: 4096, wantInline: true},
		{name: "below threshold with ref inlines and keeps ref", output: body, ref: "ref:output:aa", threshold: 4096, wantInline: true, wantRef: "ref:output:aa"},
		{name: "above threshold with ref goes by reference", output: big, ref: "ref:output:bb", threshold: 8, wantInline: false, wantRef: "ref:output:bb"},
		// INV-AG-10 data-loss guard: above threshold but the content write
		// failed (no ref) - the body must still be inlined, never dropped.
		{name: "above threshold without ref must inline", output: big, ref: "", threshold: 8, wantInline: true},
		// threshold=0 means "always use refs", but only when a ref exists.
		{name: "zero threshold with ref goes by reference", output: body, ref: "ref:output:cc", threshold: 0, wantInline: false, wantRef: "ref:output:cc"},
		{name: "zero threshold without ref must inline", output: body, ref: "", threshold: 0, wantInline: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var tr dispatchTaskResult
			setOutputFields(&tr, tc.output, tc.ref, tc.threshold)

			if tc.wantInline {
				raw, ok := tr.Output.(json.RawMessage)
				if !ok || string(raw) != string(tc.output) {
					t.Fatalf("Output = %#v, want inline %s", tr.Output, tc.output)
				}
				if tr.Synopsis != "" || tr.ReadHint != "" || tr.OutputBytes != 0 {
					t.Fatalf("inline result carries by-ref fields: %+v", tr)
				}
			} else {
				if tr.Output != nil {
					t.Fatalf("Output = %#v, want omitted (by-reference)", tr.Output)
				}
				if tr.OutputBytes != len(tc.output) {
					t.Fatalf("OutputBytes = %d, want %d", tr.OutputBytes, len(tc.output))
				}
				if tr.Synopsis == "" {
					t.Fatal("by-reference result carries no synopsis")
				}
			}
			if tr.OutputRef != tc.wantRef {
				t.Fatalf("OutputRef = %q, want %q", tr.OutputRef, tc.wantRef)
			}
			// The read hint appears exactly when the body went by-reference
			// because it was too big (not for threshold=0 ref preference).
			wantHint := tc.threshold > 0 && len(tc.output) > tc.threshold && tc.ref != ""
			if (tr.ReadHint != "") != wantHint {
				t.Fatalf("ReadHint = %q, wantHint=%v", tr.ReadHint, wantHint)
			}
		})
	}
}

func TestSetErrorFieldsErrorInlineVsRef(t *testing.T) {
	short := "boom"
	long := strings.Repeat("e", 100)

	t.Run("short error inlines and keeps error_ref", func(t *testing.T) {
		var tr dispatchTaskResult
		setErrorFields(&tr, short, nil, "", "ref:error:aa", 4096)
		if tr.Error != short || tr.ErrorRef != "ref:error:aa" {
			t.Fatalf("got Error=%q ErrorRef=%q", tr.Error, tr.ErrorRef)
		}
	})

	t.Run("long error with ref goes by reference only", func(t *testing.T) {
		var tr dispatchTaskResult
		setErrorFields(&tr, long, nil, "", "ref:error:bb", 8)
		if tr.Error != "" {
			t.Fatalf("Error = %q, want omitted (by-reference)", tr.Error)
		}
		if tr.ErrorRef != "ref:error:bb" {
			t.Fatalf("ErrorRef = %q", tr.ErrorRef)
		}
	})

	t.Run("long error without ref must inline", func(t *testing.T) {
		var tr dispatchTaskResult
		setErrorFields(&tr, long, nil, "", "", 8)
		if tr.Error != long {
			t.Fatalf("Error = %q, want the full message (no ref to point at)", tr.Error)
		}
	})

	t.Run("partial output on a failed task uses the output rules", func(t *testing.T) {
		var tr dispatchTaskResult
		out := []byte(`"partial reply"`)
		setErrorFields(&tr, short, out, "ref:output:cc", "ref:error:dd", 4096)
		raw, ok := tr.Output.(json.RawMessage)
		if !ok || string(raw) != string(out) {
			t.Fatalf("Output = %#v, want inline partial reply", tr.Output)
		}
		if tr.OutputRef != "ref:output:cc" {
			t.Fatalf("OutputRef = %q", tr.OutputRef)
		}
	})
}

func TestSetErrorFieldsSchemaViolationNeverInlinesOutput(t *testing.T) {
	// A known-malformed body must never ride the envelope inline, even far
	// below the threshold; only the ref path and structured metadata may
	// surface.
	malformed := []byte(`{"elapsed":"1.5s","steps":3,"oops":"not the schema"}`)

	t.Run("with ref exposes ref and metadata only", func(t *testing.T) {
		tr := dispatchTaskResult{Reason: "schema_violation"}
		setErrorFields(&tr, "schema violation", malformed, "ref:output:ee", "", 4096)
		if tr.Output != nil {
			t.Fatalf("Output = %#v, want never inlined for a schema violation", tr.Output)
		}
		if tr.OutputRef != "ref:output:ee" || tr.OutputBytes != len(malformed) {
			t.Fatalf("got OutputRef=%q OutputBytes=%d", tr.OutputRef, tr.OutputBytes)
		}
		if tr.Schema != "violation" {
			t.Fatalf("Schema = %q, want violation", tr.Schema)
		}
		if tr.Elapsed != "1.5s" || tr.Steps != 3 {
			t.Fatalf("structured metadata not unpacked: %+v", tr)
		}
		if tr.Synopsis != "" {
			t.Fatalf("Synopsis = %q, want empty (no preview of a malformed body)", tr.Synopsis)
		}
	})

	t.Run("without ref exposes no body at all", func(t *testing.T) {
		tr := dispatchTaskResult{Reason: "schema_violation"}
		setErrorFields(&tr, "schema violation", malformed, "", "", 4096)
		if tr.Output != nil || tr.OutputRef != "" || tr.Synopsis != "" {
			t.Fatalf("malformed body leaked without a ref: %+v", tr)
		}
	})
}

func TestSetErrorFieldsStatusDefaulting(t *testing.T) {
	t.Run("empty status falls back to failed", func(t *testing.T) {
		var tr dispatchTaskResult
		setErrorFields(&tr, "boom", nil, "", "", 4096)
		if tr.Status != string(ledger.TaskStatusFailed) {
			t.Fatalf("Status = %q, want %q", tr.Status, ledger.TaskStatusFailed)
		}
	})
	t.Run("preset status is preserved", func(t *testing.T) {
		tr := dispatchTaskResult{Status: string(ledger.TaskStatusCanceled)}
		setErrorFields(&tr, "boom", nil, "", "", 4096)
		if tr.Status != string(ledger.TaskStatusCanceled) {
			t.Fatalf("Status = %q, want preserved canceled", tr.Status)
		}
	})
}

func TestEncodeOneDispatchResultStatusDefaults(t *testing.T) {
	t.Run("unerrored result defaults to completed", func(t *testing.T) {
		tr := EncodeOneDispatchResult(subagents.Result{TaskID: "t1"}, nil, 4096)
		if tr.Status != string(ledger.TaskStatusCompleted) {
			t.Fatalf("Status = %q, want completed", tr.Status)
		}
	})
	t.Run("errored result without status reports failed", func(t *testing.T) {
		tr := EncodeOneDispatchResult(subagents.Result{TaskID: "t1", Err: errors.New("boom")}, nil, 4096)
		if tr.Status != string(ledger.TaskStatusFailed) {
			t.Fatalf("Status = %q, want failed", tr.Status)
		}
		if tr.Error != "boom" {
			t.Fatalf("Error = %q", tr.Error)
		}
	})
	t.Run("explicit status survives an error", func(t *testing.T) {
		r := subagents.Result{TaskID: "t1", Status: "canceled", Err: errors.New("canceled by operator")}
		tr := EncodeOneDispatchResult(r, nil, 4096)
		if tr.Status != "canceled" {
			t.Fatalf("Status = %q, want canceled", tr.Status)
		}
	})
	t.Run("schema violation error marks the schema field", func(t *testing.T) {
		r := subagents.Result{TaskID: "t1", Err: subagents.ErrSchemaViolation}
		tr := EncodeOneDispatchResult(r, nil, 4096)
		if tr.Reason != "schema_violation" || tr.Schema != "violation" {
			t.Fatalf("got Reason=%q Schema=%q, want schema_violation/violation", tr.Reason, tr.Schema)
		}
	})
}
