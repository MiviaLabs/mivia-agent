package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// Failures expose a bounded status only, never raw provider/tool/error bodies.
func TestDispatcherFailUsesBoundedReferences(t *testing.T) {
	d := New(Policy{})
	errBody := errors.New("accessing secret-like path is blocked: .env")
	if err := d.Register(Tool, "read_file", handlerFunc(func(context.Context, Request) (json.RawMessage, error) {
		return json.RawMessage("error: accessing secret-like path is blocked: .env"), errBody
	})); err != nil {
		t.Fatal(err)
	}
	r := d.Invoke(context.Background(), Request{ID: "t1", Kind: Tool, Name: "read_file", Input: json.RawMessage(`{"path":".env"}`)})
	if r.Err == nil {
		t.Fatal("expected error")
	}
	if len(r.Output) == 0 || strings.Contains(string(r.Output), "blocked") || strings.Contains(string(r.Output), ".env") || strings.Contains(string(r.Output), "secret-like") {
		t.Fatalf("raw failure body leaked in Output=%q", r.Output)
	}
	var payload map[string]string
	if err := json.Unmarshal(r.Output, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["status"] != "failed" || len(payload) != 1 {
		t.Fatalf("payload=%v, want exactly {status: failed}", payload)
	}
	if strings.Contains(string(r.Output), "ref:") {
		t.Fatalf("failure payload minted a reference nothing stores: %q", r.Output)
	}
	if r.Metadata.Status != "failed" {
		t.Fatalf("status=%q", r.Metadata.Status)
	}
}

func TestDispatcherFailSynthesizesOutputWhenHandlerReturnsEmpty(t *testing.T) {
	d := New(Policy{})
	if err := d.Register(Tool, "x", handlerFunc(func(context.Context, Request) (json.RawMessage, error) {
		return nil, errors.New("boom")
	})); err != nil {
		t.Fatal(err)
	}
	r := d.Invoke(context.Background(), Request{Kind: Tool, Name: "x", Input: json.RawMessage(`{}`)})
	if r.Err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(string(r.Output), "boom") {
		t.Fatalf("raw error leaked in Output=%q", r.Output)
	}
	var payload map[string]string
	if err := json.Unmarshal(r.Output, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["status"] != "failed" || len(payload) != 1 {
		t.Fatalf("payload=%v, want exactly {status: failed}", payload)
	}
	if strings.Contains(string(r.Output), "ref:") {
		t.Fatalf("failure payload minted a reference nothing stores: %q", r.Output)
	}
}

// INV-AG-10: a reference handed to the model resolves, or it is not handed to
// the model. No component at this layer can store content — the dispatcher has
// no repository, and the only non-test callers of the content store live in
// internal/coordinator — so no reference may be minted here. The failure payload
// therefore carries a status and nothing else, and the correlation value stays
// in the non-model-facing audit metadata.
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
	if payload["status"] != "failed" || len(payload) != 1 {
		t.Fatalf("payload=%v, want exactly {status: failed}", payload)
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
