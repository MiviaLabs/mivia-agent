package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// Failures must keep Output so parent agent/UI can show the reason.
func TestDispatcherFailPreservesToolOutput(t *testing.T) {
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
	if len(r.Output) == 0 {
		t.Fatal("Output must not be empty on fail — parent would hang without a reason")
	}
	if !strings.Contains(string(r.Output), "blocked") || !strings.Contains(string(r.Output), ".env") {
		t.Fatalf("Output=%q", r.Output)
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
	if !strings.Contains(string(r.Output), "boom") {
		t.Fatalf("Output=%q", r.Output)
	}
}
