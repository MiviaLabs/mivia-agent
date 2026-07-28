package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// Failures expose bounded status/references, never raw provider/tool/error bodies.
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
	if payload["status"] != "failed" || !strings.HasPrefix(payload["error_ref"], "ref:error:") || !strings.HasPrefix(payload["output_ref"], "ref:output:") {
		t.Fatalf("payload=%v", payload)
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
	if payload["status"] != "failed" || !strings.HasPrefix(payload["error_ref"], "ref:error:") {
		t.Fatalf("payload=%v", payload)
	}
}
