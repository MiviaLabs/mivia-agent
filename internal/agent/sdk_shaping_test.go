package agent

// Tests for the host-side turn-level result shaping on the SDK backend
// (applyTurnShaping): the CLI's degrade-with-notice contract replaces
// the SDK's omit-on-budget behavior. These run on the default (SDK)
// backend deliberately - the legacy twins live in
// batch_budget_integration_test.go.

import (
	"strings"
	"testing"

	sdkagentloop "github.com/MiviaLabs/mivia-ai-sdk/agentloop"
)

// TestSDKTurnShapingNeverDropsACall pins the core contract on the SDK
// backend: three over-budget results are all present in history (the
// SDK's own TurnResultBudget OMITS over-budget results), and the bytes
// entering history stay bounded by budget + degrade floor + notices.
func TestSDKTurnShapingNeverDropsACall(t *testing.T) {
	const budget = 128 << 10
	f := newBatchFixture(t, []int{200 << 10, 200 << 10, 200 << 10})
	loop := f.run(t, Options{BatchResultBudgetBytes: budget})

	bodies := toolBodies(loop)
	if len(bodies) != 3 {
		t.Fatalf("got %d tool results, want 3 - shaping must never drop a call", len(bodies))
	}
	total := totalToolBytes(loop)
	bound := budget + BatchDegradeFloorBytes + 3*(256+statusLineMaxBytes)
	if total > bound {
		t.Fatalf("turn put %d bytes in history, over the bound %d", total, bound)
	}
	for id, body := range bodies {
		if !strings.Contains(body, "truncated") && len(body) == 200<<10 {
			t.Fatalf("call %s kept its full 200 KiB body under a 128 KiB budget", id)
		}
	}
}

// TestSDKTurnShapingNoticeIsHonest pins the degrade notice: a degraded
// body names its true total and (with a live spool) a remainder ref
// the model can read back - never a bare "omitted".
func TestSDKTurnShapingNoticeIsHonest(t *testing.T) {
	const budget = 16 << 10
	f := newBatchFixture(t, []int{300 << 10, 300 << 10})
	loop := f.run(t, Options{BatchResultBudgetBytes: budget})

	bodies := toolBodies(loop)
	if len(bodies) != 2 {
		t.Fatalf("got %d tool results, want 2", len(bodies))
	}
	degraded := 0
	for id, body := range bodies {
		if strings.Contains(body, "omitted") {
			t.Fatalf("call %s was omitted by the budget: %q", id, tail(body))
		}
		if strings.Contains(body, "truncated") || strings.Contains(body, "ref:output:") {
			degraded++
		}
	}
	if degraded == 0 {
		t.Fatal("no result degraded although the turn was ~600 KiB over a 16 KiB budget")
	}
}

// TestSDKTurnShapingZeroIsInert pins the golden rule on the SDK
// backend: a zero budget means no shaping at all - every byte in
// history is exactly what the tool produced.
func TestSDKTurnShapingZeroIsInert(t *testing.T) {
	unset := newBatchFixture(t, []int{200 << 10, 200 << 10}).run(t, Options{})
	if got := totalToolBytes(unset); got < 2*(200<<10) {
		t.Fatalf("unbudgeted turn put %d bytes in history, want the full ~400 KiB", got)
	}
}

// TestSDKTurnShapingKeepsToolsOffered pins the SchemaTool contract on
// the shaping wrapper: wrapping must not drop tools from the SDK's
// offered definitions (same class as the ref-only shim finding).
func TestSDKTurnShapingKeepsToolsOffered(t *testing.T) {
	f := newBatchFixture(t, []int{1 << 10})
	loop := f.h.newLoop()
	_ = loop
	sdkOpts, err := buildAgentLoopOptions(loop, Options{
		Model: "m", BatchResultBudgetBytes: 16 << 10,
		SessionID: budgetTestSession, RemainderSpool: f.spool,
	})
	if err != nil {
		t.Fatal(err)
	}
	if sdkOpts.TurnResultBudget != 0 {
		t.Fatalf("TurnResultBudget = %d, want 0 while host shaping is active", sdkOpts.TurnResultBudget)
	}
	defs, _, err := sdkagentloop.Definitions(sdkOpts.Tools, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) == 0 {
		t.Fatal("shaping wrapper dropped every tool from the offered definitions")
	}
}
