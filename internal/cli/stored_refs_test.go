package cli

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// INV-AG-10: a reference handed to the model resolves, or it is not handed to
// the model.
//
// When a content write fails, the coordinator deliberately records no reference
// on the task. The model-visible emitters must respect that. Re-minting the
// digest from the in-memory bytes - which is what they used to do - produces a
// well-formed reference that resolves to nothing, and `not_found` then stops
// meaning "the bytes are absent".
//
// Regression: INV-AG-10
func TestModelVisibleRefsOmittedWhenContentWriteFailed(t *testing.T) {
	// A task record with blank refs is exactly the state persistResultContent
	// leaves behind when StoreContent fails.
	tasks := []ledger.TaskSnapshot{{TaskID: "t1", Status: "completed", OutputRef: "", ErrorRef: ""}}
	results := []subagents.Result{{
		TaskID: "t1", Status: "completed",
		Output: json.RawMessage(`{"finding":"x"}`),
		Err:    errors.New("boom"),
	}}

	got := modelTaskResults(tasks, results)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].OutputRef != "" {
		t.Fatalf("output_ref = %q, want empty: nothing was stored under it", got[0].OutputRef)
	}
	if got[0].ErrorRef != "" {
		t.Fatalf("error_ref = %q, want empty: nothing was stored under it", got[0].ErrorRef)
	}
	// The content itself is still delivered inline, so dropping the reference
	// costs the model nothing it needed.
	if got[0].Output == nil {
		t.Fatal("output must still be included inline")
	}
	if got[0].Error != "boom" {
		t.Fatalf("error = %q, want the error text inline", got[0].Error)
	}

	raw := (&dispatchTasksTool{}).encodeResults(tasks, results)
	var encoded []map[string]any
	if err := json.Unmarshal([]byte(raw), &encoded); err != nil {
		t.Fatalf("unmarshal %q: %v", raw, err)
	}
	if _, ok := encoded[0]["output_ref"]; ok {
		t.Fatalf("dispatch_tasks emitted an output_ref for unstored content: %s", raw)
	}
	if _, ok := encoded[0]["error_ref"]; ok {
		t.Fatalf("dispatch_tasks emitted an error_ref for unstored content: %s", raw)
	}
}

// The recorded reference is authoritative whenever the task record exists, so a
// successful run's model-visible reference is the key the content was filed
// under - not a digest recomputed alongside it.
func TestModelVisibleRefsUseRecordedValue(t *testing.T) {
	output := json.RawMessage(`{"ok":true}`)
	stored := ledger.Reference(ledger.RefKindOutput, output)
	tasks := []ledger.TaskSnapshot{{TaskID: "t1", Status: "completed", OutputRef: stored}}
	results := []subagents.Result{{TaskID: "t1", Status: "completed", Output: output}}

	if got := modelTaskResults(tasks, results)[0].OutputRef; got != stored {
		t.Fatalf("output_ref = %q, want the recorded %q", got, stored)
	}
}

// With no task record at all - a live result the ledger has not caught up on -
// canonical minting is the only available answer and must still be canonical.
func TestStoredResultRefsFallsBackToCanonicalMinting(t *testing.T) {
	output := json.RawMessage(`{"ok":true}`)
	outputRef, errorRef := storedResultRefs(nil, subagents.Result{
		TaskID: "absent", Output: output, Err: errors.New("bad"),
	})
	if want := ledger.Reference(ledger.RefKindOutput, output); outputRef != want {
		t.Fatalf("output ref = %q, want %q", outputRef, want)
	}
	if want := ledger.Reference(ledger.RefKindError, []byte("bad")); errorRef != want {
		t.Fatalf("error ref = %q, want %q", errorRef, want)
	}
}
