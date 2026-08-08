package provider

import (
	"context"
	"math"
	"strings"
	"testing"
)

func TestNewRequestRejectsAnEmptyModel(t *testing.T) {
	c := NewOpenAICompatWithOptions(CompatOptions{Name: "test", BaseURL: "https://example.test", APIKey: "k"})
	_, err := c.newRequest(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err == nil || !strings.Contains(err.Error(), "model is required") {
		t.Fatalf("error = %v, want a model-required failure", err)
	}
}

// A tool spec is caller-supplied data reaching json.Marshal. An unencodable
// value must surface as an error from request building rather than panic or
// produce a truncated body.
func TestNewRequestReportsAnUnencodableToolSpec(t *testing.T) {
	c := NewOpenAICompatWithOptions(CompatOptions{Name: "test", BaseURL: "https://example.test", APIKey: "k"})
	_, err := c.newRequest(context.Background(), Request{
		Model:    "m",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
		Tools:    []ToolSpec{{"type": "function", "bad": math.NaN()}},
	})
	if err == nil {
		t.Fatal("an unencodable tool spec must fail request building")
	}
}

func TestNewRequestReportsAnUnusableBaseURL(t *testing.T) {
	c := NewOpenAICompatWithOptions(CompatOptions{Name: "test", BaseURL: "https://exa\x7fmple.test", APIKey: "k"})
	_, err := c.newRequest(context.Background(), Request{
		Model:    "m",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("an unparseable URL must fail request building")
	}
}

// OpenRouter identifies callers with these two headers, so they are the
// difference between an attributed request and an anonymous one.
func TestNewRequestSetsAttributionAndStreamHeaders(t *testing.T) {
	c := NewOpenAICompatWithOptions(CompatOptions{
		Name: "test", BaseURL: "https://example.test", APIKey: "k",
		HTTPReferer: "https://github.com/MiviaLabs/mivia-agent", XTitle: "Mivia Agent",
		ExtraHeaders: map[string]string{"X-Extra": "1"},
	})
	httpReq, err := c.newRequest(context.Background(), Request{
		Model:    "m",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
		Stream:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for header, want := range map[string]string{
		"HTTP-Referer": "https://github.com/MiviaLabs/mivia-agent",
		"X-Title":      "Mivia Agent",
		"X-Extra":      "1",
		"Accept":       "text/event-stream",
		"Content-Type": "application/json",
	} {
		if got := httpReq.Header.Get(header); got != want {
			t.Fatalf("%s = %q, want %q", header, got, want)
		}
	}
	if key := httpReq.Header.Get("Idempotency-Key"); !strings.HasPrefix(key, "mivia-") {
		t.Fatalf("Idempotency-Key = %q", key)
	}
}

func TestNewRequestRejectsReservedExtras(t *testing.T) {
	cases := map[string]CompatOptions{
		"reserved header": {ExtraHeaders: map[string]string{"authorization": "nope"}},
		"reserved body":   {ExtraBody: map[string]any{"messages": "nope"}},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			opts.Name = "test"
			opts.BaseURL = "https://example.test"
			opts.APIKey = "k"
			c := NewOpenAICompatWithOptions(opts)
			_, err := c.newRequest(context.Background(), Request{
				Model:    "m",
				Messages: []Message{{Role: RoleUser, Content: "hi"}},
			})
			if err == nil || !strings.Contains(err.Error(), "reserved") {
				t.Fatalf("error = %v, want a reserved-key failure", err)
			}
		})
	}
}
