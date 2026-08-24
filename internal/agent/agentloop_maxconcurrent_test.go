package agent

// Integration tests for the MaxConcurrentTools wire-through. The
// SDK's overlapTool (mivia-ai-sdk/agentloop/parallel_tools_test.go)
// is package-private, so the host replicates the determinism
// pattern - entry WaitGroup and release channel - on a CLI-shaped
// test tool that the SDK dispatcher can drive in parallel.

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// overlapRecordingTool is the CLI-shaped analogue of the SDK
// overlapTool: Execute bumps a guarded in-flight counter, records
// the observed maximum per tool, signals entry on the WaitGroup,
// and blocks on the release channel until the test closes it.
// That entry/release pair lets a test hold every call inside
// Execute until it has observed the pool's full overlap, with no
// time.Sleep at all (semgrep rule "time.Sleep in tests" forbids
// the obvious alternative). Capability returns ExecutionRead so
// the approval gate and any read-class skip-dedup check skip it.
type overlapRecordingTool struct {
	name string

	mu      sync.Mutex
	cur     int
	max     int
	idOrder []string

	// entered, when non-nil, is Done once per Execute on entry.
	entered *sync.WaitGroup
	// release, when non-nil, blocks every Execute until closed.
	// Without it the tool returns immediately and the test cannot
	// observe a parallel peak.
	release chan struct{}
}

func (t *overlapRecordingTool) Name() string               { return t.name }
func (t *overlapRecordingTool) Description() string        { return "overlap recording tool" }
func (t *overlapRecordingTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (t *overlapRecordingTool) Capability(json.RawMessage) tools.Capability {
	return tools.Capability{Class: tools.ExecutionRead, ResourceKey: "path:" + t.name}
}

func (t *overlapRecordingTool) Execute(ctx context.Context, _ json.RawMessage) (string, error) {
	t.mu.Lock()
	t.cur++
	if t.cur > t.max {
		t.max = t.cur
	}
	t.idOrder = append(t.idOrder, t.name)
	t.mu.Unlock()
	if t.entered != nil {
		t.entered.Done()
	}
	if t.release != nil {
		<-t.release
	}
	t.mu.Lock()
	t.cur--
	t.mu.Unlock()
	return "ok", nil
}

// maxInflight returns the highest in-flight Execute count observed.
func (t *overlapRecordingTool) maxInflight() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.max
}

// threeCallTurn builds a registry of three overlapRecordingTools
// named a/b/c and a scriptCompleter whose first response requests
// one call per tool in Index order (slice order maps to SDK Index
// in cliMessageToSDK, agentloop_completer.go).
func threeCallTurn(t *testing.T) (*overlapRecordingTool, *overlapRecordingTool, *overlapRecordingTool, *tools.Registry, *scriptCompleter) {
	t.Helper()
	a := &overlapRecordingTool{name: "a"}
	b := &overlapRecordingTool{name: "b"}
	c := &overlapRecordingTool{name: "c"}
	reg := tools.NewRegistry()
	reg.Register(a)
	reg.Register(b)
	reg.Register(c)
	comp := &scriptCompleter{steps: []provider.Response{
		{
			FinishReason: "tool_calls",
			ToolCalls: []provider.ToolCall{
				tc("call-a", "a", `{}`),
				tc("call-b", "b", `{}`),
				tc("call-c", "c", `{}`),
			},
		},
		{Content: "final", FinishReason: "stop"},
	}}
	return a, b, c, reg, comp
}

// historyToolCallIDsFromLoop returns the RoleTool ToolCallIDs the
// SDK-backed run appended to loop.Messages, in history order.
func historyToolCallIDsFromLoop(l *Loop) []string {
	ids := make([]string, 0, 3)
	for _, m := range l.Messages {
		if m.Role == provider.RoleTool {
			ids = append(ids, m.ToolCallID)
		}
	}
	return ids
}

// assertHistoryIndexOrder fails unless ids is exactly want. The
// SDK adapter stamps the per-tool RoleTool message's Name from the
// assistant's ToolCall that requested it (stampSDKToolMessageNames),
// so the test reads the IDs and the names - both should fall in
// the original Index order regardless of execution overlap.
func assertHistoryIndexOrder(t *testing.T, l *Loop, want []string) {
	t.Helper()
	ids := historyToolCallIDsFromLoop(l)
	if len(ids) != len(want) {
		t.Fatalf("history tool IDs = %v, want %v", ids, want)
	}
	for i, id := range want {
		if ids[i] != id {
			t.Fatalf("history[%d] = %q, want %q: path must preserve Index order in history", i, ids[i], id)
		}
	}
	// Name must also follow Index order so the host-side stamp
	// (loop_dispatch.go's SDK write-back) routes messages by name,
	// not just ID. Walk the messages skipping non-tool roles and
	// comparing only the k-th tool message's Name.
	wantNames := []string{"a", "b", "c"}
	k := 0
	for _, m := range l.Messages {
		if m.Role != provider.RoleTool {
			continue
		}
		if k >= len(wantNames) || m.Name != wantNames[k] {
			t.Fatalf("RoleTool[%d].Name = %q, want %q", k, m.Name, wantNames[k])
		}
		k++
	}
}

// waitedGuard waits for wg with a timeout so a deadlocked (serially
// starved) pool fails the test instead of hanging it.
func waitedGuard(t *testing.T, wg *sync.WaitGroup) {
	t.Helper()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("calls never all entered Execute within 5s: dispatch did not overlap")
	}
}

// TestMaxConcurrentToolsSDKCarriesHostValue proves a positive host
// Options.MaxConcurrentTools fans the SDK path out through a
// worker pool of N: a turn with three distinct calls holds all
// three inside Execute at the same instant (observed maximum
// in-flight count 3) and history still follows ToolCall.Index
// order. Without the wire-through this test would still observe
// max-in-flight == 1 on the SDK backend - the same as the legacy
// default - and fail.
func TestMaxConcurrentToolsSDKCarriesHostValue(t *testing.T) {
	a, b, c, reg, comp := threeCallTurn(t)
	entered := &sync.WaitGroup{}
	entered.Add(3)
	release := make(chan struct{})
	for _, tl := range []*overlapRecordingTool{a, b, c} {
		tl.entered = entered
		tl.release = release
	}
	loop := &Loop{Completer: comp, Tools: reg}
	type runResult struct {
		out string
		err error
	}
	done := make(chan runResult, 1)
	go func() {
		out, err := loop.Run(context.Background(), "hi", Options{
			Model:              "m",
			MaxSteps:           5,
			MaxConcurrentTools: 3,
		})
		done <- runResult{out, err}
	}()
	waitedGuard(t, entered)
	if got := a.maxInflight() + b.maxInflight() + c.maxInflight(); got != 3 {
		t.Fatalf("sum of per-tool max in-flight = %d, want 3: each tool must hold exactly one call", got)
	}
	close(release)
	out := <-done
	if out.err != nil {
		t.Fatalf("Run() error = %v, want nil", out.err)
	}
	if out.out != "final" {
		t.Fatalf("Run() output = %q, want %q", out.out, "final")
	}
	assertHistoryIndexOrder(t, loop, []string{"call-a", "call-b", "call-c"})
}

// TestMaxConcurrentToolsHostOneRemainsSerial proves MaxConcurrentTools
// 1 keeps the SDK path serial: a turn with three distinct calls
// never has more than one Execute in flight, and history follows
// ToolCall.Index order. This is the regression guard for the
// "explicit 1" caller, mirroring the SDK's own TestMaxConcurrentToolsSerialPreservesOrder.
func TestMaxConcurrentToolsHostOneRemainsSerial(t *testing.T) {
	a := &overlapRecordingTool{name: "a"}
	b := &overlapRecordingTool{name: "b"}
	c := &overlapRecordingTool{name: "c"}
	reg := tools.NewRegistry()
	reg.Register(a)
	reg.Register(b)
	reg.Register(c)
	comp := &scriptCompleter{steps: []provider.Response{
		{
			FinishReason: "tool_calls",
			ToolCalls: []provider.ToolCall{
				tc("call-a", "a", `{}`),
				tc("call-b", "b", `{}`),
				tc("call-c", "c", `{}`),
			},
		},
		{Content: "final", FinishReason: "stop"},
	}}
	loop := &Loop{Completer: comp, Tools: reg}
	_, err := loop.Run(context.Background(), "hi", Options{
		Model:              "m",
		MaxSteps:           5,
		MaxConcurrentTools: 1,
	})
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	for _, tl := range []*overlapRecordingTool{a, b, c} {
		if got := tl.maxInflight(); got != 1 {
			t.Fatalf("%s max in-flight = %d, want 1: MaxConcurrentTools=1 must keep serial dispatch", tl.name, got)
		}
	}
	assertHistoryIndexOrder(t, loop, []string{"call-a", "call-b", "call-c"})
}

// silence unused-import warnings if test bodies shrink during refactor.
var _ = atomic.Int32{}
