package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
)

func TestDialectBodyFields(t *testing.T) {
	cases := []struct {
		name    string
		dialect reasoning.Dialect
		level   reasoning.Level
		want    map[string]any
	}{
		{"openai unset", reasoning.DialectOpenAI, "", nil},
		{"openai off", reasoning.DialectOpenAI, reasoning.Off, map[string]any{"reasoning_effort": "none"}},
		{"openai high", reasoning.DialectOpenAI, reasoning.High, map[string]any{"reasoning_effort": "high"}},
		{"openai max", reasoning.DialectOpenAI, reasoning.Max, map[string]any{"reasoning_effort": "max"}},

		{"openrouter unset", reasoning.DialectOpenRouter, "", nil},
		{"openrouter off", reasoning.DialectOpenRouter, reasoning.Off, map[string]any{"reasoning": map[string]any{"enabled": false}}},
		{"openrouter low", reasoning.DialectOpenRouter, reasoning.Low, map[string]any{"reasoning": map[string]any{"effort": "low"}}},

		{"thinking unset", reasoning.DialectThinking, "", nil},
		{"thinking off", reasoning.DialectThinking, reasoning.Off, map[string]any{"thinking": map[string]any{"type": "disabled"}}},
		{"thinking medium", reasoning.DialectThinking, reasoning.Medium, map[string]any{"thinking": map[string]any{"type": "enabled"}}},

		{"thinking_effort unset", reasoning.DialectThinkingEffort, "", nil},
		{"thinking_effort off", reasoning.DialectThinkingEffort, reasoning.Off, map[string]any{"thinking": map[string]any{"type": "disabled"}}},
		{"thinking_effort xhigh", reasoning.DialectThinkingEffort, reasoning.XHigh, map[string]any{
			"thinking":         map[string]any{"type": "enabled"},
			"reasoning_effort": "xhigh",
		}},

		{"none never emits", reasoning.DialectNone, reasoning.High, nil},
		{"unset dialect never emits", "", reasoning.High, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := reasoningBodyFields(tc.dialect, tc.level)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("reasoningBodyFields(%q, %q) = %#v, want %#v", tc.dialect, tc.level, got, tc.want)
			}
		})
	}
}

// thinking_effort disables with the thinking object alone. Sending
// reasoning_effort: "none" alongside a disabled thinking object would be two
// contradictory instructions in one body.
func TestThinkingEffortOffSendsNoEffortKey(t *testing.T) {
	got := reasoningBodyFields(reasoning.DialectThinkingEffort, reasoning.Off)
	if _, present := got["reasoning_effort"]; present {
		t.Fatalf("off must not carry an effort value, got %#v", got)
	}
}

// captureBody runs one non-streaming turn against a stub and returns the
// decoded request body.
func captureBody(t *testing.T, opts CompatOptions, req Request) map[string]any {
	t.Helper()
	var raw []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()
	opts.BaseURL = srv.URL
	if opts.Name == "" {
		opts.Name = "test"
	}
	c := NewOpenAICompatWithOptions(opts)
	if _, err := c.ChatTurn(context.Background(), req); err != nil {
		t.Fatalf("ChatTurn: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode captured body: %v (%s)", err, raw)
	}
	return body
}

func baseRequest() Request {
	temp := 0.0
	return Request{
		Model:       "m",
		Messages:    []Message{{Role: RoleUser, Content: "hi"}},
		Temperature: &temp,
	}
}

// The backwards-compatibility proof: a request with no reasoning level must
// serialize exactly as it did before reasoning existed, on a client that HAS a
// default dialect.
func TestUnsetLevelLeavesBodyUnchanged(t *testing.T) {
	withDialect := captureBody(t, CompatOptions{Reasoning: reasoning.DialectThinking}, baseRequest())
	withoutDialect := captureBody(t, CompatOptions{}, baseRequest())
	if !reflect.DeepEqual(withDialect, withoutDialect) {
		t.Fatalf("a client with a default dialect changed an unset-level body:\n%#v\n---\n%#v", withDialect, withoutDialect)
	}
	for _, key := range []string{"reasoning", "reasoning_effort", "thinking"} {
		if _, present := withDialect[key]; present {
			t.Fatalf("unset level emitted %q: %#v", key, withDialect)
		}
	}
	if _, present := withDialect["temperature"]; !present {
		t.Fatal("temperature must survive; the stub proves the baseline carries it")
	}
}

// Sampling suppression is deliberately absent. The premise that reasoning
// models reject temperature was disproved against current provider docs, and
// deleting a field the provider accepts would change valid requests.
func TestActiveReasoningKeepsSamplingParameters(t *testing.T) {
	req := baseRequest()
	req.ReasoningLevel = reasoning.High
	body := captureBody(t, CompatOptions{Reasoning: reasoning.DialectThinking}, req)
	temp, present := body["temperature"]
	if !present {
		t.Fatalf("active reasoning removed temperature: %#v", body)
	}
	if temp != float64(0) {
		t.Fatalf("temperature = %v, want 0", temp)
	}
}

func TestClientDefaultDialectAppliesWhenRequestOmitsOne(t *testing.T) {
	req := baseRequest()
	req.ReasoningLevel = reasoning.High
	body := captureBody(t, CompatOptions{Reasoning: reasoning.DialectThinking}, req)
	thinking, ok := body["thinking"].(map[string]any)
	if !ok || thinking["type"] != "enabled" {
		t.Fatalf("client default dialect did not shape the body: %#v", body)
	}
}

func TestRequestDialectOverridesClientDefault(t *testing.T) {
	req := baseRequest()
	req.ReasoningLevel = reasoning.High
	req.ReasoningDialect = reasoning.DialectOpenAI
	body := captureBody(t, CompatOptions{Reasoning: reasoning.DialectThinking}, req)
	if body["reasoning_effort"] != "high" {
		t.Fatalf("request dialect did not win: %#v", body)
	}
	if _, present := body["thinking"]; present {
		t.Fatalf("the overridden client dialect still emitted: %#v", body)
	}
}

// A level with nowhere to go emits nothing rather than guessing a wire shape.
// Configuration refuses this combination up front; the client still fails
// closed if it ever arrives.
func TestActiveLevelWithNoResolvableDialectEmitsNothing(t *testing.T) {
	req := baseRequest()
	req.ReasoningLevel = reasoning.High
	body := captureBody(t, CompatOptions{}, req)
	for _, key := range []string{"reasoning", "reasoning_effort", "thinking"} {
		if _, present := body[key]; present {
			t.Fatalf("emitted %q with no resolvable dialect: %#v", key, body)
		}
	}
}

func TestExplicitNoneDialectOverridesClientDefault(t *testing.T) {
	req := baseRequest()
	req.ReasoningLevel = reasoning.High
	req.ReasoningDialect = reasoning.DialectNone
	body := captureBody(t, CompatOptions{Reasoning: reasoning.DialectThinking}, req)
	if _, present := body["thinking"]; present {
		t.Fatalf("an explicit none dialect must silence the client default: %#v", body)
	}
}

// Reasoning merges after ExtraBody, so a model-scoped level wins over a static
// key naming the same field. Without a stated order this is a coin flip that
// changes with map iteration.
func TestActiveReasoningWinsOverCollidingExtraBody(t *testing.T) {
	req := baseRequest()
	req.ReasoningLevel = reasoning.Low
	body := captureBody(t, CompatOptions{
		Reasoning: reasoning.DialectOpenAI,
		ExtraBody: map[string]any{"reasoning_effort": "max"},
	}, req)
	if body["reasoning_effort"] != "low" {
		t.Fatalf("ExtraBody beat the model-scoped level: %#v", body)
	}
}

// ExtraBody keeps working as the escape hatch whenever no level is active, so
// existing configurations driving reasoning through it are untouched.
func TestExtraBodySurvivesWhenNoLevelIsActive(t *testing.T) {
	body := captureBody(t, CompatOptions{
		Reasoning: reasoning.DialectOpenAI,
		ExtraBody: map[string]any{"reasoning_effort": "max"},
	}, baseRequest())
	if body["reasoning_effort"] != "max" {
		t.Fatalf("ExtraBody was dropped: %#v", body)
	}
}

func TestBuildingARequestDoesNotMutateTheCaller(t *testing.T) {
	req := baseRequest()
	req.ReasoningLevel = reasoning.High
	before := req
	_ = captureBody(t, CompatOptions{Reasoning: reasoning.DialectThinkingEffort}, req)
	if !reflect.DeepEqual(req, before) {
		t.Fatalf("caller request mutated:\n%#v\n---\n%#v", req, before)
	}
}

// A stream that yields nothing falls back to a non-streaming call built from a
// fresh Request. Omitting the reasoning fields there silently downgrades the
// model on exactly the turn that already went wrong.
func TestStreamFallbackCarriesReasoning(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var decoded map[string]any
		_ = json.Unmarshal(raw, &decoded)
		bodies = append(bodies, decoded)
		if decoded["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	c := NewOpenAICompatWithOptions(CompatOptions{Name: "test", BaseURL: srv.URL, APIKey: "k", Reasoning: reasoning.DialectThinking})
	req := baseRequest()
	req.ReasoningLevel = reasoning.High
	req.ReasoningDialect = reasoning.DialectOpenAI
	if _, err := c.ChatStream(context.Background(), req, io.Discard); err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if len(bodies) != 2 {
		t.Fatalf("expected a stream attempt and a fallback, got %d requests", len(bodies))
	}
	fallback := bodies[1]
	if fallback["stream"] != false {
		t.Fatalf("second request was not the non-streaming fallback: %#v", fallback)
	}
	if fallback["reasoning_effort"] != "high" {
		t.Fatalf("fallback dropped the reasoning fields: %#v", fallback)
	}
}

func TestProviderConstructorDialects(t *testing.T) {
	cases := []struct {
		name string
		ctor func(Options) (Completer, error)
		want reasoning.Dialect
	}{
		{"zai", NewZAI, reasoning.DialectThinking},
		{"openrouter", NewOpenRouter, reasoning.DialectOpenAI},
		// DeepSeek stays unset until reasoning_content replay exists.
		{"deepseek", NewDeepSeek, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			comp, err := tc.ctor(Options{APIKey: "k"})
			if err != nil {
				t.Fatalf("constructor: %v", err)
			}
			client, ok := comp.(*OpenAICompat)
			if !ok {
				t.Fatalf("%s did not return an OpenAICompat", tc.name)
			}
			if client.reasoning != tc.want {
				t.Fatalf("%s default dialect = %q, want %q", tc.name, client.reasoning, tc.want)
			}
		})
	}
}

// The constructor defaults must agree with the config-facing table, or a model
// validated against reasoning.DefaultDialect would reach a client that sends
// something else.
func TestConstructorDefaultsMatchTheConfigFacingTable(t *testing.T) {
	if err := registerBuiltins(); err != nil {
		t.Fatalf("registerBuiltins: %v", err)
	}
	for _, name := range builtinFactories.names() {
		want, _ := reasoning.DefaultDialect(name)
		factory, ok := builtinFactories.lookup(name)
		if !ok {
			t.Fatalf("%s: no registered factory", name)
		}
		comp, err := factory(Options{APIKey: "k"})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		client, ok := comp.(*OpenAICompat)
		if !ok {
			t.Fatalf("%s did not return an OpenAICompat", name)
		}
		if client.reasoning != want {
			t.Fatalf("%s client dialect %q disagrees with DefaultDialect %q", name, client.reasoning, want)
		}
	}
}

// factoryBody runs one turn against a stub through a provider FACTORY, so what
// is asserted is the dialect the shipped client carries rather than one the
// test handed it.
func factoryBody(t *testing.T, build func(Options) (Completer, error), req Request) map[string]any {
	t.Helper()
	var raw []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()
	c, err := build(Options{BaseURL: srv.URL, APIKey: "k"})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	if _, err := c.ChatTurn(context.Background(), req); err != nil {
		t.Fatalf("ChatTurn: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode captured body: %v (%s)", err, raw)
	}
	return body
}

// The factories read their dialect from the table config validates against
// instead of naming one. These pin the wire shape that indirection must not
// change: zai gates with a thinking object, openrouter sends the shorthand.
func TestProviderFactoriesCarryTheirVettedDialect(t *testing.T) {
	req := baseRequest()
	req.ReasoningLevel = reasoning.High

	zai := factoryBody(t, NewZAI, req)
	thinking, ok := zai["thinking"].(map[string]any)
	if !ok || thinking["type"] != "enabled" {
		t.Fatalf("zai did not gate thinking on: %#v", zai)
	}
	if _, present := zai["reasoning_effort"]; present {
		t.Fatalf("the thinking dialect must not carry an effort value: %#v", zai)
	}

	router := factoryBody(t, NewOpenRouter, req)
	if router["reasoning_effort"] != "high" {
		t.Fatalf("openrouter did not send the shorthand: %#v", router)
	}
}

// A client built without a default dialect still encodes what config validated
// for its provider, because both read the same table. Were it otherwise, a
// model entry that omits reasoning_dialect would pass load and then send
// nothing.
func TestClientWithNoDefaultResolvesFromTheProviderName(t *testing.T) {
	req := baseRequest()
	req.ReasoningLevel = reasoning.High
	body := captureBody(t, CompatOptions{Name: "zai"}, req)
	if thinking, ok := body["thinking"].(map[string]any); !ok || thinking["type"] != "enabled" {
		t.Fatalf("an unqualified level on zai sent %#v", body)
	}
	// A provider outside the table has no vetted shape to fall back to, so its
	// body must be the one a request with no level at all produces.
	unvetted := captureBody(t, CompatOptions{Name: "deepseek"}, req)
	if baseline := captureBody(t, CompatOptions{Name: "deepseek"}, baseRequest()); !reflect.DeepEqual(unvetted, baseline) {
		t.Fatalf("an unvetted provider invented a reasoning field:\n%#v\n---\n%#v", unvetted, baseline)
	}
}
