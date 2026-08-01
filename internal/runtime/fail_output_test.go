package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// Failures surface the full, unredacted error reason to the model, plus a
// bounded status - never raw provider/tool OUTPUT bodies, and never a content
// reference nothing can resolve.
//
// The error string is safe to surface verbatim because mivia's own tool/handler
// code authors those messages and is required by rule 10 to keep secrets out of
// them: secret-path errors are static ("... is blocked") and embed no operand.
// Opaquing all error reasons into {"status":"failed"} (the prior contract)
// made every failure indistinguishable and forced blind retry - the model could
// not tell a bad path from a missing argument from a broken tool.
func TestDispatcherFailSurfacesErrorReason(t *testing.T) {
	d := New(Policy{})
	const errText = "path \"/etc/passwd\" escapes workspace"
	errBody := errors.New(errText)
	if err := d.Register(Tool, "read_file", handlerFunc(func(context.Context, Request) (json.RawMessage, error) {
		return nil, errBody
	})); err != nil {
		t.Fatal(err)
	}
	r := d.Invoke(context.Background(), Request{ID: "t1", Kind: Tool, Name: "read_file", Input: json.RawMessage(`{"path":"/etc/passwd"}`)})
	if r.Err == nil {
		t.Fatal("expected error")
	}
	var payload map[string]string
	if err := json.Unmarshal(r.Output, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["status"] != "failed" {
		t.Fatalf("status=%q, want failed", payload["status"])
	}
	// The raw error reason must reach the model so it can self-correct.
	if payload["error"] != errText {
		t.Fatalf("error reason not surfaced: got %q, want %q", payload["error"], errText)
	}
	// No content reference may be minted - nothing at this layer stores bytes,
	// so a ref handed to the model would be unresolvable (INV-AG-10).
	if strings.Contains(string(r.Output), "ref:") {
		t.Fatalf("failure payload minted a reference nothing stores: %q", r.Output)
	}
	for _, key := range []string{"error_ref", "output_ref"} {
		if _, ok := payload[key]; ok {
			t.Fatalf("payload minted %s=%q that nothing stores", key, payload[key])
		}
	}
	if r.Metadata.Status != "failed" {
		t.Fatalf("metadata status=%q", r.Metadata.Status)
	}
}

// A handler that produced OUTPUT bytes before failing: the output body stays
// out of the model-facing payload (it may contain provider/tool content), but
// the error reason is still surfaced. The output bytes survive only in the
// non-model-facing audit metadata (hash + preview).
func TestDispatcherFailSurfacesErrorButNotOutputBody(t *testing.T) {
	d := New(Policy{})
	if err := d.Register(Tool, "x", handlerFunc(func(context.Context, Request) (json.RawMessage, error) {
		return json.RawMessage(`{"partial":"sensitive-output-body"}`), errors.New("handler failed mid-stream")
	})); err != nil {
		t.Fatal(err)
	}
	r := d.Invoke(context.Background(), Request{Kind: Tool, Name: "x", Input: json.RawMessage(`{}`)})
	if r.Err == nil {
		t.Fatal("expected error")
	}
	var payload map[string]string
	if err := json.Unmarshal(r.Output, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["status"] != "failed" {
		t.Fatalf("status=%q, want failed", payload["status"])
	}
	// Error reason surfaced.
	if payload["error"] != "handler failed mid-stream" {
		t.Fatalf("error reason not surfaced: got %q", payload["error"])
	}
	// Output body must NOT leak into the model-facing payload.
	if strings.Contains(string(r.Output), "sensitive-output-body") {
		t.Fatalf("raw output body leaked in Output=%q", r.Output)
	}
	if strings.Contains(string(r.Output), "ref:") {
		t.Fatalf("failure payload minted a reference nothing stores: %q", r.Output)
	}
}

// INV-AG-10: a reference handed to the model resolves, or it is not handed to
// the model. No component at this layer can store content - the dispatcher has
// no repository, and the only non-test callers of the content store live in
// internal/coordinator - so no reference may be minted here. The failure payload
// carries a status and the error reason; the correlation value (output hash +
// preview) stays in the non-model-facing audit metadata.
func TestDispatcherFailureOmitsUnstoredRefs(t *testing.T) {
	const errText = "handler exploded while reading config"
	var events []Event
	d := New(Policy{Sink: func(e Event) { events = append(events, e) }})
	if err := d.Register(Tool, "boom", handlerFunc(func(context.Context, Request) (json.RawMessage, error) {
		return json.RawMessage(`{"partial":"body"}`), errors.New(errText)
	})); err != nil {
		t.Fatal(err)
	}
	r := d.Invoke(context.Background(), Request{Kind: Tool, Name: "boom", Input: json.RawMessage(`{}`)})
	if r.Err == nil {
		t.Fatal("expected error")
	}
	var payload map[string]string
	if err := json.Unmarshal(r.Output, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["status"] != "failed" {
		t.Fatalf("status=%q, want failed", payload["status"])
	}
	if payload["error"] != errText {
		t.Fatalf("error reason not surfaced: got %q, want %q", payload["error"], errText)
	}
	for _, key := range []string{"error_ref", "output_ref"} {
		if _, ok := payload[key]; ok {
			t.Fatalf("payload minted %s=%q that nothing stores", key, payload[key])
		}
	}
	if strings.Contains(string(r.Output), "ref:") {
		t.Fatalf("payload contains a reference: %q", r.Output)
	}
	// The bounded correlation value survives where it belongs: audit metadata.
	if r.Metadata.OutputHash == "" || r.Metadata.OutputPreview == "" {
		t.Fatalf("audit correlation lost: hash=%q preview=%q", r.Metadata.OutputHash, r.Metadata.OutputPreview)
	}
	if len(events) == 0 {
		t.Fatal("expected the failure to reach the sink")
	}
}
