package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/remainder"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// Aggregate per-batch tool-result budget, end to end (plan tools/06).
//
// These run the real agent loop against the real httptest provider, the real
// tool registry, and a real remainder spool. The unit tests in
// shape_batch_test.go pin the enforcement core; these pin the thing the plan
// is actually about - that N parallel calls landing in ONE batch cannot
// jointly blow the context, and that nothing the model paid for is lost in the
// process.

const budgetTestSession = "session-batch-budget"

// batchFixture writes n files of size bytes each and returns the read_file
// calls that fetch them, plus the loop options the batch runs under.
type batchFixture struct {
	h     *integrationHelper
	spool *remainder.Spool
	store *remainder.MemoryStore
	calls []provider.ToolCall
	bytes map[string]string // call id → the file's true content
}

func newBatchFixture(t *testing.T, sizes []int) *batchFixture {
	t.Helper()
	calls := make([]provider.ToolCall, len(sizes))
	for i := range sizes {
		calls[i] = toolCall(fmt.Sprintf("call_read_%d", i), "read_file",
			fmt.Sprintf(`{"path":"big%d.txt"}`, i))
	}
	h := newIntegrationHelperWithOpts(t, []scriptedStep{
		{content: "reading the files", toolCalls: calls},
		{content: "read them all"},
	}, tools.DefaultOptions{MaxReadBytes: 4 << 20})

	store := remainder.NewMemoryStore()
	f := &batchFixture{h: h, spool: remainder.NewSpool(store), store: store, calls: calls, bytes: map[string]string{}}
	for i, size := range sizes {
		// Distinct filler per file so a body cannot be confused for a sibling's.
		content := strings.Repeat(string(rune('a'+i%26)), size)
		if err := os.WriteFile(filepath.Join(h.ws.Abs, fmt.Sprintf("big%d.txt", i)), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		f.bytes[calls[i].ID] = content
	}
	return f
}

func (f *batchFixture) run(t *testing.T, opts Options) *Loop {
	t.Helper()
	loop := f.h.newLoop()
	if opts.Model == "" {
		opts.Model = "integration-model"
	}
	if opts.MaxSteps == 0 {
		opts.MaxSteps = 5
	}
	if opts.MaxConcurrentTools == 0 {
		opts.MaxConcurrentTools = 4
	}
	if opts.ToolTimeout == 0 {
		opts.ToolTimeout = 20 * time.Second
	}
	if opts.SessionID == "" {
		opts.SessionID = budgetTestSession
	}
	if opts.RemainderSpool == nil {
		opts.RemainderSpool = f.spool
	}
	if _, err := loop.Run(context.Background(), "read the files", opts); err != nil {
		t.Fatal(err)
	}
	return loop
}

func toolBodies(loop *Loop) map[string]string {
	out := map[string]string{}
	for _, msg := range loop.Messages {
		if msg.Role == provider.RoleTool {
			out[msg.ToolCallID] = msg.Content
		}
	}
	return out
}

func totalToolBytes(loop *Loop) int {
	n := 0
	for _, body := range toolBodies(loop) {
		n += len(body)
	}
	return n
}

// TestIntegration_BatchBudgetZeroIsInert is the golden: with the knob unset,
// every byte in history is exactly what it is today. This is the test that
// fails first if shaping ever leaks onto the default path.
func TestIntegration_BatchBudgetZeroIsInert(t *testing.T) {
	sizes := []int{200 << 10, 200 << 10, 200 << 10}
	unset := newBatchFixture(t, sizes).run(t, Options{})
	zero := newBatchFixture(t, sizes).run(t, Options{BatchResultBudgetBytes: 0})

	unsetBodies, zeroBodies := toolBodies(unset), toolBodies(zero)
	if len(unsetBodies) != len(sizes) {
		t.Fatalf("got %d tool results, want %d", len(unsetBodies), len(sizes))
	}
	for id, body := range unsetBodies {
		if zeroBodies[id] != body {
			t.Fatalf("call %s: budget=0 changed the result (len %d vs %d)", id, len(zeroBodies[id]), len(body))
		}
		if !strings.Contains(body, strings.Repeat("a", 1024)) && !strings.Contains(body, strings.Repeat("b", 1024)) && !strings.Contains(body, strings.Repeat("c", 1024)) {
			t.Fatalf("call %s did not return real file bytes: head=%q", id, head(body))
		}
	}
	if got := totalToolBytes(unset); got < 3*(200<<10) {
		t.Fatalf("unbudgeted batch put %d bytes in history, want the full ~600 KiB", got)
	}
}

// The plan's goal sentence, tested directly: three calls, each honestly under
// its own per-call cap, must not jointly blow the batch budget.
func TestIntegration_ParallelBatchCannotJointlyBlowTheBudget(t *testing.T) {
	const budget = 128 << 10
	f := newBatchFixture(t, []int{200 << 10, 200 << 10, 200 << 10})
	loop := f.run(t, Options{Backend: "legacy", BatchResultBudgetBytes: budget})

	bodies := toolBodies(loop)
	if len(bodies) != 3 {
		t.Fatalf("got %d tool results, want 3 - shaping must never drop a call", len(bodies))
	}
	total := totalToolBytes(loop)
	bound := budget + BatchDegradeFloorBytes + 3*(256+statusLineMaxBytes)
	if total > bound {
		t.Fatalf("batch put %d bytes in history, over the bound %d", total, bound)
	}
	// Nothing was destroyed: every degraded body still names a live remainder.
	degraded := 0
	for id, b := range bodies {
		if !strings.Contains(b, "... truncated: kept ") {
			continue
		}
		degraded++
		ref := refIn(t, b)
		data, err := f.spool.Load(context.Background(), budgetTestSession, ref)
		if err != nil {
			t.Fatalf("call %s: ref %s does not resolve for the emitting principal: %v", id, ref, err)
		}
		if string(data) != f.bytes[id] {
			t.Fatalf("call %s: ref pages %d bytes, want the original %d", id, len(data), len(f.bytes[id]))
		}
		assertNoPartialRef(t, b)
	}
	if degraded == 0 {
		t.Fatal("no result degraded although the batch was 600 KiB over a 128 KiB budget")
	}
	if got := strings.Count(strings.Join(valuesOf(bodies), "\x00"), "batch result budget"); got != 1 {
		t.Fatalf("status line appears %d times, want exactly 1", got)
	}
}

// F6: the bound must not depend on MaxToolCallsPerBatch. A huge batch with the
// count guard OFF still lands inside budget + floor + framing.
func TestIntegration_HugeBatchStaysBoundedWithNoCallCountLimit(t *testing.T) {
	const (
		batchSize = 40
		budget    = 64 << 10
	)
	sizes := make([]int, batchSize)
	for i := range sizes {
		sizes[i] = 32 << 10
	}
	f := newBatchFixture(t, sizes)
	loop := f.run(t, Options{
		BatchResultBudgetBytes: budget,
		MaxToolCallsPerBatch:   0,
		MaxConcurrentTools:     8,
	})

	bodies := toolBodies(loop)
	if len(bodies) != batchSize {
		t.Fatalf("got %d tool results, want %d - no call may be failed by the budget", len(bodies), batchSize)
	}
	total := totalToolBytes(loop)
	bound := budget + BatchDegradeFloorBytes + batchSize*(256+statusLineMaxBytes)
	if total > bound {
		t.Fatalf("%d-call batch put %d bytes in history, over the finite bound %d", batchSize, total, bound)
	}
	// Sanity: the unbudgeted same batch really is far larger, so the bound
	// above is not passing for lack of data.
	unbudgeted := newBatchFixture(t, sizes).run(t, Options{})
	if got := totalToolBytes(unbudgeted); got <= total {
		t.Fatalf("unbudgeted batch is %d bytes, budgeted %d - the fixture proves nothing", got, total)
	}
}

// Every degraded result must page back byte-identically through the same grant
// domain read_output uses (D9): the ref covers the ORIGINAL body, never a
// truncation artifact.
func TestIntegration_DegradedResultRefPagesTheOriginalBytes(t *testing.T) {
	f := newBatchFixture(t, []int{4 << 20, 300 << 10})
	loop := f.run(t, Options{
		BatchResultBudgetBytes: 32 << 10,
		MaxToolResultChars:     128 << 10, // pass 1 truncates the first read
	})

	bodies := toolBodies(loop)
	for id, b := range bodies {
		if !strings.Contains(b, "ref:output:") {
			continue
		}
		if n := strings.Count(b, "... truncated: kept "); n != 1 {
			t.Fatalf("call %s carries %d truncation notices, want exactly 1: tail=%q", id, n, tail(b))
		}
		if !strings.Contains(b, fmt.Sprintf("of %d bytes", len(f.bytes[id]))) {
			t.Fatalf("call %s lost the true original total (%d bytes): tail=%q", id, len(f.bytes[id]), tail(b))
		}
		data, err := f.spool.Load(context.Background(), budgetTestSession, refIn(t, b))
		if err != nil {
			t.Fatalf("call %s: ref does not resolve: %v", id, err)
		}
		if string(data) != f.bytes[id] {
			t.Fatalf("call %s: ref pages a truncation artifact, not the original", id)
		}
		assertNoPartialRef(t, b)
	}
}

// D5: refs are SessionID-scoped. A second loop, another session, cannot page a
// remainder minted for the first - the isolation the whole ref mechanism rests
// on. Parent and child loops deliberately DO share, because a subagent runs
// under its parent's SessionID.
func TestIntegration_RemainderRefsAreInvisibleAcrossSessions(t *testing.T) {
	f := newBatchFixture(t, []int{300 << 10})
	loop := f.run(t, Options{Backend: "legacy", BatchResultBudgetBytes: 16 << 10, SessionID: "session-A"})

	body := toolBodies(loop)["call_read_0"]
	ref := refIn(t, body)
	if _, err := f.spool.Load(context.Background(), "session-A", ref); err != nil {
		t.Fatalf("emitting principal cannot read its own remainder: %v", err)
	}
	if _, err := f.spool.Load(context.Background(), "session-B", ref); err == nil {
		t.Fatal("a remainder minted for session-A resolved for session-B")
	}
}

// Determinism (F4): identical batches, identical store outcomes, identical
// bytes in history.
func TestIntegration_ShapedBatchIsDeterministic(t *testing.T) {
	sizes := []int{300 << 10, 90 << 10, 150 << 10, 20 << 10}
	opts := Options{BatchResultBudgetBytes: 100 << 10}
	first := toolBodies(newBatchFixture(t, sizes).run(t, opts))
	second := toolBodies(newBatchFixture(t, sizes).run(t, opts))
	for id, body := range first {
		if second[id] != body {
			t.Fatalf("call %s differs between identical runs:\n%q\nvs\n%q", id, tail(body), tail(second[id]))
		}
	}
}

// Pairing survives shaping: every tool_call_id gets exactly one result
// message, which is what keeps the next provider request legal. A status line
// that shipped as its own message would break here (S3).
func TestIntegration_ShapedBatchKeepsToolPairingIntact(t *testing.T) {
	sizes := []int{300 << 10, 300 << 10, 300 << 10}
	f := newBatchFixture(t, sizes)
	loop := f.run(t, Options{BatchResultBudgetBytes: 32 << 10})

	seen := map[string]int{}
	for _, msg := range loop.Messages {
		if msg.Role == provider.RoleTool {
			seen[msg.ToolCallID]++
			if msg.ToolCallID == "" {
				t.Fatal("an orphan RoleTool message entered history")
			}
			if strings.TrimSpace(msg.Content) == "" {
				t.Fatalf("tool result %q is empty - a call's result was destroyed", msg.Name)
			}
		}
	}
	for _, call := range f.calls {
		if seen[call.ID] != 1 {
			t.Fatalf("tool_call_id %q got %d result messages, want exactly 1", call.ID, seen[call.ID])
		}
	}
	// Pruning after a degraded batch must still leave a legal history.
	pruned := provider.PruneMessagesKeepTurns(loop.Messages, 1000, provider.ContextAccountingProfile{})
	assertToolPairing(t, pruned)
}

// C7: a worker-synthesized error body is charged like any other result and
// survives shaping intact - the model must still be told the call failed.
func TestIntegration_FailedCallKeepsItsErrorTextUnderBudget(t *testing.T) {
	calls := []provider.ToolCall{
		toolCall("call_ok", "read_file", `{"path":"big0.txt"}`),
		toolCall("call_missing", "read_file", `{"path":"nope-does-not-exist.txt"}`),
	}
	h := newIntegrationHelperWithOpts(t, []scriptedStep{
		{content: "reading", toolCalls: calls},
		{content: "done"},
	}, tools.DefaultOptions{MaxReadBytes: 4 << 20})
	if err := os.WriteFile(filepath.Join(h.ws.Abs, "big0.txt"), []byte(strings.Repeat("a", 300<<10)), 0o600); err != nil {
		t.Fatal(err)
	}
	loop := h.newLoop()
	if _, err := loop.Run(context.Background(), "read them", Options{Backend: "legacy",
		Model: "integration-model", MaxSteps: 5, MaxConcurrentTools: 2,
		ToolTimeout: 20 * time.Second, SessionID: budgetTestSession,
		RemainderSpool:         remainder.NewSpool(remainder.NewMemoryStore()),
		BatchResultBudgetBytes: 16 << 10,
	}); err != nil {
		t.Fatal(err)
	}
	body := toolResultBody(loop.Messages, "call_missing")
	if body == "" {
		t.Fatal("failed call lost its result message")
	}
	if !strings.Contains(strings.ToLower(body), "error") && !strings.Contains(body, "nope-does-not-exist") {
		t.Fatalf("failed call's body no longer explains the failure: %q", head(body))
	}
}

// Hook context rides above the budget (C4/F5): it is re-appended after
// shaping, uncut, and never depletes the tool byte budget.
func TestIntegration_HookContextSurvivesShapingUncut(t *testing.T) {
	f := newBatchFixture(t, []int{300 << 10, 300 << 10})
	const advice = "REMEMBER-THE-HOUSE-STYLE"
	loop := f.h.newLoop()
	dispatcher := hookContextDispatcher(t, f.h, strings.Repeat(advice+" ", 40))
	if _, err := loop.Run(context.Background(), "read the files", Options{Backend: "legacy",
		Model: "integration-model", MaxSteps: 5, MaxConcurrentTools: 2,
		ToolTimeout: 20 * time.Second, SessionID: budgetTestSession,
		RemainderSpool: f.spool, Dispatcher: dispatcher,
		BatchResultBudgetBytes: 16 << 10,
	}); err != nil {
		t.Fatal(err)
	}
	for id, body := range toolBodies(loop) {
		if !strings.Contains(body, hookOutputOpenTag) || !strings.Contains(body, hookOutputCloseTag) {
			t.Fatalf("call %s lost its framed hook block under shaping: tail=%q", id, tail(body))
		}
		if got := strings.Count(body, advice); got != 40 {
			t.Fatalf("call %s: hook context was cut (%d of 40 repetitions survived)", id, got)
		}
	}
}

// bigEphemeralTool stands in for a resource-bearing tool (skill resources):
// its body is large, and ScrubEphemeralToolMessages replaces it with a marker
// once the turn ends.
type bigEphemeralTool struct{ size int }

func (t *bigEphemeralTool) Name() string        { return "ephemeral_resource" }
func (t *bigEphemeralTool) Description() string { return "returns a large ephemeral body" }
func (t *bigEphemeralTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}
}

func (t *bigEphemeralTool) Execute(context.Context, json.RawMessage) (string, error) {
	return strings.Repeat("z", t.size), nil
}

func (t *bigEphemeralTool) EphemeralResultMarker(json.RawMessage) string {
	return "[resource body omitted]"
}

// D10, end to end: an ephemeral result is charged against the batch budget
// like any other, but its bytes are never put behind a ref. A ref would
// outlive the scrub and let the model page back, through read_output, exactly
// the bytes the scrub exists to remove.
//
// MaxToolResultChars is set so PASS 1 truncates the 300 KiB ephemeral body:
// the bug this pins is that pass 1 spooled the full body behind a ref BEFORE
// the ephemeral detection, so the truncated notice named ref:output:<digest>
// and the bytes sat in the store - for read_output to resurrect - no matter
// what the batch shaper or the scrub did afterwards.
func TestIntegration_EphemeralResultIsChargedButNeverReferenced(t *testing.T) {
	calls := []provider.ToolCall{
		toolCall("call_read_0", "read_file", `{"path":"big0.txt"}`),
		toolCall("call_ephemeral", "ephemeral_resource", `{}`),
	}
	h := newIntegrationHelperWithOpts(t, []scriptedStep{
		{content: "reading", toolCalls: calls},
		{content: "done"},
	}, tools.DefaultOptions{MaxReadBytes: 4 << 20})
	h.reg.Register(&bigEphemeralTool{size: 300 << 10})
	if err := os.WriteFile(filepath.Join(h.ws.Abs, "big0.txt"), []byte(strings.Repeat("a", 300<<10)), 0o600); err != nil {
		t.Fatal(err)
	}
	store := remainder.NewMemoryStore()
	spool := remainder.NewSpool(store)

	loop := h.newLoop()
	if _, err := loop.Run(context.Background(), "read them", Options{Backend: "legacy",
		Model: "integration-model", MaxSteps: 5, MaxConcurrentTools: 2,
		ToolTimeout: 20 * time.Second, SessionID: budgetTestSession,
		RemainderSpool: spool, BatchResultBudgetBytes: 32 << 10,
		// Pass 1 truncates both 300 KiB bodies. The read_file body is
		// legitimately spooled behind a ref; the ephemeral body must NOT be.
		MaxToolResultChars: 8 << 10,
	}); err != nil {
		t.Fatal(err)
	}

	body := toolResultBody(loop.Messages, "call_ephemeral")
	if body == "" {
		t.Fatal("ephemeral call lost its result message")
	}
	// (a) The truncated body's notice names NO remainder: pass 1 must have
	// capped the ephemeral body with a plain notice, so neither the shaped
	// body nor its truncation notice may carry a ref or the read_output
	// directive that goes with one.
	if strings.Contains(body, "ref:output:") {
		t.Fatalf("ephemeral result was put behind a ref: tail=%q", tail(body))
	}
	if strings.Contains(body, "use read_output") {
		t.Fatalf("ephemeral result's truncation notice directs the model to a remainder: tail=%q", tail(body))
	}
	if !strings.Contains(body, "... truncated: kept ") {
		t.Fatalf("ephemeral result was degraded without an honest notice: tail=%q", tail(body))
	}
	// (b) The ephemeral body is not in the store under ANY ref: refs are
	// content addressed, so its own digest is the only key it could occupy.
	if data, err := store.LoadContent(context.Background(),
		sdkadapter.Mint(sdkadapter.KindOutput, []byte(strings.Repeat("z", 300<<10)))); err == nil {
		t.Fatalf("the ephemeral body is retrievable from the store (%d bytes)", len(data))
	}

	ScrubEphemeralToolMessages(loop.Messages, h.reg)
	if got := toolResultBody(loop.Messages, "call_ephemeral"); got != "[resource body omitted]" {
		t.Fatalf("post-scrub body = %q, want the marker", head(got))
	}
}

func valuesOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

// refIn extracts the single content reference a shaped body names.
func refIn(t *testing.T, body string) string {
	t.Helper()
	i := strings.Index(body, "ref:output:")
	if i < 0 {
		t.Fatalf("body names no remainder ref: tail=%q", tail(body))
	}
	ref := body[i:]
	if j := strings.IndexAny(ref, " ,)\n"); j >= 0 {
		ref = ref[:j]
	}
	return ref
}

// assertToolPairing fails when a tool result has no announcing assistant call.
func assertToolPairing(t *testing.T, messages []provider.Message) {
	t.Helper()
	announced := map[string]bool{}
	for _, msg := range messages {
		if msg.Role == provider.RoleAssistant {
			for _, call := range msg.ToolCalls {
				announced[call.ID] = true
			}
			continue
		}
		if msg.Role == provider.RoleTool && !announced[msg.ToolCallID] {
			t.Fatalf("orphan tool result %q survived pruning", msg.ToolCallID)
		}
	}
}

// hookContextDispatcher returns a real dispatcher whose tool invocations carry
// PostToolUse advisory context, so shaping is exercised against results that
// really have hook bytes attached.
func hookContextDispatcher(t *testing.T, h *integrationHelper, advice string) *runtime.Dispatcher {
	t.Helper()
	d, err := runtime.NewToolDispatcher(h.reg, runtime.Policy{
		PostInvokeHook: func(context.Context, runtime.Request, runtime.Result) runtime.HookResult {
			return runtime.HookResult{Context: advice}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return d
}
