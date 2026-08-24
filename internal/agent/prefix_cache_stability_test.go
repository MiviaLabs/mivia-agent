package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// ---------------------------------------------------------------------------
// Prompt-cache prefix stability regression harness
// ---------------------------------------------------------------------------
// These tests pin the wire-level invariant a provider's implicit prompt
// cache depends on: within one turn, step N's serialized messages array must
// be a byte-for-byte prefix of step N+1's, with divergence confined to the
// newly appended tail. A real httptest server + the real OpenAICompat client
// are used so every request round-trips through the real (unexported)
// marshalBody, not a stand-in. CacheMarkersEnabled is left false (the
// default): with markers off the invariant is literal; with markers on,
// markRollingBreakpoint deliberately rewrites an older message's content as
// the rolling breakpoint advances, which is a different, already-documented
// contract (see openai_compat_request.go) out of scope here.

// requestCapture records every raw HTTP request body sent to a capturing
// server, in arrival order.
type requestCapture struct {
	mu   sync.Mutex
	raws [][]byte
}

func (c *requestCapture) add(raw []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := make([]byte, len(raw))
	copy(cp, raw)
	c.raws = append(c.raws, cp)
}

func (c *requestCapture) all() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([][]byte, len(c.raws))
	copy(out, c.raws)
	return out
}

// newCapturingServer is newIntegrationServer's response-scripting shape
// (loop_integration_test.go's scriptedStep, reused directly) plus raw
// request-body capture, so callers can inspect the exact bytes marshalBody
// produced for each step.
func newCapturingServer(t *testing.T, steps []scriptedStep) (*httptest.Server, *requestCapture) {
	t.Helper()
	capture := &requestCapture{}
	var callCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		capture.add(raw)

		idx := int(callCount.Load())
		callCount.Add(1)
		if idx >= len(steps) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{
					"finish_reason": "stop",
					"message":       map[string]string{"role": "assistant", "content": "done (fallthrough)"},
				}},
			})
			return
		}
		step := steps[idx]
		if len(step.toolCalls) == 0 {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{
					"finish_reason": "stop",
					"message":       map[string]string{"role": "assistant", "content": step.content},
				}},
			})
			return
		}
		toolCallMaps := make([]map[string]any, len(step.toolCalls))
		for i, tc := range step.toolCalls {
			toolCallMaps[i] = map[string]any{
				"id": tc.ID, "type": "function",
				"function": map[string]string{"name": tc.Function.Name, "arguments": tc.Function.Arguments},
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"finish_reason": "tool_calls",
				"message":       map[string]any{"role": "assistant", "content": step.content, "tool_calls": toolCallMaps},
			}},
		})
	}))
	t.Cleanup(srv.Close)
	return srv, capture
}

// stepWire is one captured request's decoded messages array (element-wise,
// not flattened) and raw tools array. Comparing the decoded messages array
// element-by-element (rather than whole-body byte prefixes) is deliberate:
// marshalBody round-trips the typed payload through map[string]any, so
// json.Marshal re-sorts every key alphabetically (top-level: max_tokens,
// messages, model, stream, tool_choice, tools) - messages sits ahead of
// several static fields, so a whole-body byte-prefix comparison would fail
// on every step even with zero regressions, purely because growing the
// messages array shifts the byte offset of everything after it.
type stepWire struct {
	messages []json.RawMessage
	tools    json.RawMessage
}

func decodeStepWire(t *testing.T, raw []byte) stepWire {
	t.Helper()
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatalf("decode top-level request body: %v", err)
	}
	var messages []json.RawMessage
	if err := json.Unmarshal(top["messages"], &messages); err != nil {
		t.Fatalf("decode messages array: %v", err)
	}
	return stepWire{messages: messages, tools: top["tools"]}
}

func decodeStepWires(t *testing.T, raws [][]byte) []stepWire {
	t.Helper()
	out := make([]stepWire, len(raws))
	for i, raw := range raws {
		out[i] = decodeStepWire(t, raw)
	}
	return out
}

// firstDivergingIndex returns the first index where a and b differ as raw
// bytes, or the length of the shorter slice if every common element
// matches (all divergence, if any, is pure tail growth).
func firstDivergingIndex(a, b []json.RawMessage) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if !bytes.Equal(a[i], b[i]) {
			return i
		}
	}
	return n
}

func rawMessageName(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var m struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode message name: %v", err)
	}
	return m.Name
}

// splitTrailingNamed peels the trailing NAMED messages (host injections -
// the summary, then the conclude nudge, per stepRequest's pinned order) off
// the end of a decoded messages array, mirroring markRollingBreakpoint's own
// distinction between user-typed/structural content and host injections.
func splitTrailingNamed(t *testing.T, messages []json.RawMessage) (structural, named []json.RawMessage) {
	t.Helper()
	cut := len(messages)
	for cut > 0 && rawMessageName(t, messages[cut-1]) != "" {
		cut--
	}
	return messages[:cut], messages[cut:]
}

func findNamedMessage(t *testing.T, messages []json.RawMessage, name string) (json.RawMessage, bool) {
	t.Helper()
	for _, raw := range messages {
		if rawMessageName(t, raw) == name {
			return raw, true
		}
	}
	return nil, false
}

func toolCallStep(content, id, tool, args string) scriptedStep {
	return scriptedStep{
		content: content,
		toolCalls: []provider.ToolCall{{
			ID: id, Type: "function",
			Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: tool, Arguments: args},
		}},
	}
}

// TestPrefixCacheStabilityAcrossToolLoopSteps asserts, for a plain
// (non-compacting) multi-step tool loop: each step's serialized messages
// array is an exact byte-for-byte prefix of the next, growing by exactly the
// newly committed assistant/tool messages; the system message never moves;
// and the serialized tools array is byte-identical across every step.
func TestPrefixCacheStabilityAcrossToolLoopSteps(t *testing.T) {
	steps := []scriptedStep{
		toolCallStep("looking at files", "call_1", "write_file", `{"path":"a.txt","content":"a"}`),
		{
			content: "checking two things",
			toolCalls: []provider.ToolCall{
				{ID: "call_2a", Type: "function", Function: struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}{Name: "write_file", Arguments: `{"path":"b.txt","content":"b"}`}},
				{ID: "call_2b", Type: "function", Function: struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}{Name: "read_file", Arguments: `{"path":"c.txt"}`}},
			},
		},
		toolCallStep("one more", "call_3", "write_file", `{"path":"d.txt","content":"d"}`),
		{content: "all done"},
	}
	srv, capture := newCapturingServer(t, steps)
	comp := provider.NewOpenAICompatWithOptions(provider.CompatOptions{Name: "prefix-stability-test", BaseURL: srv.URL, APIKey: "test-key"})

	reg := tools.NewRegistry()
	reg.Register(&writeFakeTool{name: "write_file"})
	reg.Register(&writeFakeTool{name: "read_file"})

	loop := &Loop{
		Completer: comp,
		Tools:     reg,
		Messages: []provider.Message{
			{Role: provider.RoleSystem, Content: "you are a fixed system prompt for prefix stability testing"},
		},
	}
	if _, err := loop.Run(context.Background(), "please do the work", Options{Model: "prefix-model",
		MaxSteps:           6,
		MaxConcurrentTools: 2,
		ToolTimeout:        5 * time.Second,
	}); err != nil {
		t.Fatal(err)
	}

	raws := capture.all()
	if len(raws) != len(steps) {
		t.Fatalf("captured %d requests, want %d", len(raws), len(steps))
	}
	wires := decodeStepWires(t, raws)

	for i := range wires {
		if !bytes.Equal(wires[0].messages[0], wires[i].messages[0]) {
			t.Fatalf("system message changed by step %d", i)
		}
		if len(wires[i].tools) == 0 {
			t.Fatalf("step %d tools array unexpectedly empty", i)
		}
	}

	// Expected tail growth per step transition: one assistant tool_call
	// message plus one tool-result message per tool call issued that step.
	expectedTail := []int{2, 3, 2}
	for i := 0; i+1 < len(wires); i++ {
		idx := firstDivergingIndex(wires[i].messages, wires[i+1].messages)
		if idx != len(wires[i].messages) {
			t.Fatalf("step %d->%d diverged inside earlier history at message index %d (want pure tail growth at %d)", i, i+1, idx, len(wires[i].messages))
		}
		tail := len(wires[i+1].messages) - len(wires[i].messages)
		if tail != expectedTail[i] {
			t.Fatalf("step %d->%d tail grew by %d messages, want %d", i, i+1, tail, expectedTail[i])
		}
		if !bytes.Equal(wires[i].tools, wires[i+1].tools) {
			t.Fatalf("tools array changed between step %d and %d", i, i+1)
		}
	}
}

// TestPrefixCacheStabilityCompactionDivergesOnceThenNextTurnStabilizes
// asserts assertion 3: across a compacted turn, the injected summary
// appears starting at exactly one step boundary and never disappears again,
// its rendered bytes stay memo-stable at every later step even though its
// array index keeps moving (stepRequest re-appends it after the
// currently-longest structural prefix each step), the structural prefix
// invariant from Test 1 holds throughout, and a simulated next turn -
// seeded the way internal/chat's commitContextTurn actually commits after a
// compacted turn (system message + loop.InjectedSummary(), Name preserved)
// - re-establishes its own clean, stable prefix.
func TestPrefixCacheStabilityCompactionDivergesOnceThenNextTurnStabilizes(t *testing.T) {
	systemPrompt := "you are a fixed system prompt for prefix stability testing"
	wires, injectedSummary := runCompactingTurn1(t, systemPrompt)
	assertTurn1CompactionInvariants(t, wires)
	runAndAssertTurn2(t, systemPrompt, wires, injectedSummary)
}

// runCompactingTurn1 drives a 4-step turn that compacts on the 2nd Prepare
// call (a deterministic, time/rand-free trigger - see
// stepKeyedCompactingProbe) and returns the decoded per-step wire and the
// summary the loop reports injecting.
func runCompactingTurn1(t *testing.T, systemPrompt string) ([]stepWire, provider.Message) {
	t.Helper()
	steps := []scriptedStep{
		toolCallStep("step one", "call_1", "write_file", `{"path":"a.txt","content":"a"}`),
		toolCallStep("step two", "call_2", "write_file", `{"path":"b.txt","content":"b"}`),
		toolCallStep("step three", "call_3", "write_file", `{"path":"c.txt","content":"c"}`),
		{content: "turn one done"},
	}
	srv, capture := newCapturingServer(t, steps)
	comp := provider.NewOpenAICompatWithOptions(provider.CompatOptions{Name: "prefix-stability-compaction-test", BaseURL: srv.URL, APIKey: "test-key"})

	summ := &capturingSummaryProvider{}
	summarizer := summaryInjectSummarizer(t, summ)
	probe := &stepKeyedCompactingProbe{compactOn: map[int]bool{2: true}}
	opts := summaryProbeOptions(t, &summarizer, probe, 100_000)
	opts.Model = "prefix-model"
	opts.MaxSteps = 6
	opts.MaxConcurrentTools = 2
	opts.ToolTimeout = 5 * time.Second

	reg := tools.NewRegistry()
	reg.Register(&writeFakeTool{name: "write_file"})

	loop := &Loop{
		Completer: comp,
		Tools:     reg,
		Messages: []provider.Message{
			{Role: provider.RoleSystem, Content: systemPrompt},
		},
	}
	if _, err := loop.Run(context.Background(), "please do the work", opts); err != nil {
		t.Fatal(err)
	}
	if len(summ.requests) != 1 {
		t.Fatalf("summary provider called %d times, want exactly 1 (memoized, not regenerated per step)", len(summ.requests))
	}

	raws := capture.all()
	if len(raws) != len(steps) {
		t.Fatalf("captured %d requests, want %d", len(raws), len(steps))
	}
	wires := decodeStepWires(t, raws)

	injectedSummary, ok := loop.InjectedSummary()
	if !ok {
		t.Fatal("turn 1 loop reports no injected summary")
	}
	if injectedSummary.Name != SummaryMessageName {
		t.Fatalf("injected summary Name = %q, want %q", injectedSummary.Name, SummaryMessageName)
	}
	return wires, injectedSummary
}

// assertTurn1CompactionInvariants checks: system message and tools[] never
// move; the structural prefix (Test 1's core check, with the summary/nudge
// trailer stripped) holds across every step; and the summary appears
// starting at exactly one step boundary, staying byte-identical afterward.
func assertTurn1CompactionInvariants(t *testing.T, wires []stepWire) {
	t.Helper()
	for i := range wires {
		if !bytes.Equal(wires[0].messages[0], wires[i].messages[0]) {
			t.Fatalf("system message changed by step %d", i)
		}
		if len(wires[i].tools) == 0 {
			t.Fatalf("step %d tools array unexpectedly empty", i)
		}
		if !bytes.Equal(wires[0].tools, wires[i].tools) {
			t.Fatalf("tools array changed by step %d", i)
		}
	}

	structuralByStep := make([][]json.RawMessage, len(wires))
	namedByStep := make([][]json.RawMessage, len(wires))
	for i, w := range wires {
		structuralByStep[i], namedByStep[i] = splitTrailingNamed(t, w.messages)
	}
	for i := 0; i+1 < len(structuralByStep); i++ {
		idx := firstDivergingIndex(structuralByStep[i], structuralByStep[i+1])
		if idx != len(structuralByStep[i]) {
			t.Fatalf("structural step %d->%d diverged inside earlier history at index %d", i, i+1, idx)
		}
	}

	// The summary is absent before the compaction step, then present from
	// that step through the rest of the turn - the flip happens exactly
	// once, never toggling back off.
	if _, ok := findNamedMessage(t, namedByStep[0], SummaryMessageName); ok {
		t.Fatal("summary present before the compaction step")
	}
	var summaryBytes []json.RawMessage
	for i := 1; i < len(namedByStep); i++ {
		raw, ok := findNamedMessage(t, namedByStep[i], SummaryMessageName)
		if !ok {
			t.Fatalf("summary missing from step %d after compaction", i)
		}
		summaryBytes = append(summaryBytes, raw)
	}
	for i := 1; i < len(summaryBytes); i++ {
		if !bytes.Equal(summaryBytes[0], summaryBytes[i]) {
			t.Fatalf("memoized summary changed bytes between post-compaction steps 1 and %d", i+1)
		}
	}
}

// runAndAssertTurn2 seeds a fresh Loop the way internal/chat's
// commitContextTurn actually commits after a compacted turn (system message
// + loop.InjectedSummary() verbatim, Name preserved) and asserts the new
// turn re-establishes its own clean, stable prefix.
func runAndAssertTurn2(t *testing.T, systemPrompt string, turn1Wires []stepWire, injectedSummary provider.Message) {
	t.Helper()
	turn2Steps := []scriptedStep{
		toolCallStep("turn two working", "call_t2", "write_file", `{"path":"e.txt","content":"e"}`),
		{content: "turn two done"},
	}
	srv2, capture2 := newCapturingServer(t, turn2Steps)
	comp2 := provider.NewOpenAICompatWithOptions(provider.CompatOptions{Name: "prefix-stability-compaction-test-turn2", BaseURL: srv2.URL, APIKey: "test-key"})
	reg2 := tools.NewRegistry()
	reg2.Register(&writeFakeTool{name: "write_file"})

	loop2 := &Loop{
		Completer: comp2,
		Tools:     reg2,
		Messages: []provider.Message{
			{Role: provider.RoleSystem, Content: systemPrompt},
			injectedSummary,
		},
	}
	if _, err := loop2.Run(context.Background(), "continue the work", Options{Model: "prefix-model",
		MaxSteps:           6,
		MaxConcurrentTools: 2,
		ToolTimeout:        5 * time.Second,
	}); err != nil {
		t.Fatal(err)
	}

	raws2 := capture2.all()
	if len(raws2) != len(turn2Steps) {
		t.Fatalf("turn 2 captured %d requests, want %d", len(raws2), len(turn2Steps))
	}
	wires2 := decodeStepWires(t, raws2)

	if !bytes.Equal(turn1Wires[0].messages[0], wires2[0].messages[0]) {
		t.Fatal("turn 2 system message differs from turn 1's")
	}
	for i := 0; i+1 < len(wires2); i++ {
		idx := firstDivergingIndex(wires2[i].messages, wires2[i+1].messages)
		if idx != len(wires2[i].messages) {
			t.Fatalf("turn 2 step %d->%d diverged inside earlier history at index %d", i, i+1, idx)
		}
		if !bytes.Equal(wires2[i].tools, wires2[i+1].tools) {
			t.Fatalf("turn 2 tools array changed between step %d and %d", i, i+1)
		}
	}
}
