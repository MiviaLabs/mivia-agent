package agent

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

func TestWorkLimitMeterUsesCumulativeProviderBalances(t *testing.T) {
	meter := workLimitMeter{limits: runtime.WorkLimits{MaxPromptTokens: 5, MaxOutputTokens: 8, MaxOutputPerCall: 4}}
	if err := meter.reserveProvider(3, 4); err != nil {
		t.Fatal(err)
	}
	if err := meter.reserveProvider(2, 4); err != nil {
		t.Fatal(err)
	}
	if err := meter.reserveProvider(1, 1); err == nil {
		t.Fatal("third provider request exceeded cumulative balance")
	}
}

func TestWorkLimitMeterRejectsWholeToolBatch(t *testing.T) {
	meter := workLimitMeter{limits: runtime.WorkLimits{MaxToolCalls: 3}}
	if err := meter.reserveToolBatch(2); err != nil {
		t.Fatal(err)
	}
	if err := meter.reserveToolBatch(2); err == nil {
		t.Fatal("over-limit tool batch was accepted")
	}
	if meter.toolCalls != 2 {
		t.Fatalf("tool calls = %d, want 2", meter.toolCalls)
	}
}

func TestWorkLimitMeterZeroLimitsAreUnlimited(t *testing.T) {
	meter := workLimitMeter{}
	if err := meter.reserveProvider(1<<20, 1<<20); err != nil {
		t.Fatal(err)
	}
	if err := meter.reserveToolBatch(1 << 20); err != nil {
		t.Fatal(err)
	}
}

func TestWorkLimitMeterProviderBoundaries(t *testing.T) {
	for _, limit := range []int{4} {
		t.Run("limit", func(t *testing.T) {
			meter := workLimitMeter{limits: runtime.WorkLimits{MaxPromptTokens: limit, MaxOutputTokens: limit, MaxOutputPerCall: limit}}
			if err := meter.reserveProvider(limit-1, limit-1); err != nil {
				t.Fatalf("max-1 reservation: %v", err)
			}
			if err := meter.reserveProvider(1, 1); err != nil {
				t.Fatalf("max reservation: %v", err)
			}
			if err := meter.reserveProvider(1, 1); err == nil {
				t.Fatal("max+1 reservation succeeded")
			}
		})
	}
}

func TestWorkLimitMeterOneAllowsExactlyOneRequest(t *testing.T) {
	meter := workLimitMeter{limits: runtime.WorkLimits{MaxPromptTokens: 1, MaxOutputTokens: 1, MaxOutputPerCall: 1}}
	if err := meter.reserveProvider(1, 1); err != nil {
		t.Fatal(err)
	}
	if err := meter.reserveProvider(1, 1); err == nil {
		t.Fatal("second request exceeded one-token limit")
	}
}

func TestWorkLimitMeterReservesRemainingOutputWhenPerCallIsUnlimited(t *testing.T) {
	meter := workLimitMeter{limits: runtime.WorkLimits{MaxOutputTokens: 4}}
	if err := meter.reserveProvider(0, 0); err != nil {
		t.Fatalf("first output reservation: %v", err)
	}
	if got := meter.outputTokens; got != 4 {
		t.Fatalf("reserved output = %d, want 4", got)
	}
	if err := meter.reserveProvider(0, 0); err == nil {
		t.Fatal("second unbounded output reservation succeeded")
	}
}

func TestWorkLimitMeterOutputCapUsesPerCallAndRemainingTotal(t *testing.T) {
	meter := workLimitMeter{limits: runtime.WorkLimits{MaxOutputTokens: 5, MaxOutputPerCall: 4}}
	cap, err := meter.outputCap(nil)
	if err != nil || cap == nil || *cap != 4 {
		t.Fatalf("first cap = %v, %v; want 4, nil", cap, err)
	}
	if err := meter.reserveProvider(0, *cap); err != nil {
		t.Fatal(err)
	}
	// A positive remaining budget clamps the per-call allocation down to the
	// remainder (1 of 5) instead of erroring; only a genuinely exhausted
	// budget (remaining <= 0) rejects.
	cap, err = meter.outputCap(nil)
	if err != nil || cap == nil || *cap != 1 {
		t.Fatalf("second cap = %v, %v; want 1, nil", cap, err)
	}
	if err := meter.reserveProvider(0, *cap); err != nil {
		t.Fatal(err)
	}
	if _, err := meter.outputCap(nil); err == nil {
		t.Fatal("exhausted output allocation was accepted")
	}
}

// A requested output above the remaining cumulative budget must be clamped
// down to the remainder, not rejected: outputCap derives the maximum allocation
// for one provider request, and a positive remaining budget is an allocation.
func TestWorkLimitMeterOutputCapClampsRequestedAboveRemaining(t *testing.T) {
	meter := workLimitMeter{limits: runtime.WorkLimits{MaxOutputTokens: 4000}}

	requested := 8192
	cap, err := meter.outputCap(&requested)
	if err != nil || cap == nil || *cap != 4000 {
		t.Fatalf("requested=%d cap = %v, %v; want 4000, nil", requested, cap, err)
	}

	equal := 4000
	cap, err = meter.outputCap(&equal)
	if err != nil || cap == nil || *cap != 4000 {
		t.Fatalf("requested=%d cap = %v, %v; want 4000, nil", equal, cap, err)
	}

	below := 2048
	cap, err = meter.outputCap(&below)
	if err != nil || cap == nil || *cap != 2048 {
		t.Fatalf("requested=%d cap = %v, %v; want 2048, nil", below, cap, err)
	}
}

func TestWorkLimitMeterOutputCapUsesFiniteTotalWithoutPerCallLimit(t *testing.T) {
	meter := workLimitMeter{limits: runtime.WorkLimits{MaxOutputTokens: 5}}
	cap, err := meter.outputCap(nil)
	if err != nil || cap == nil || *cap != 5 {
		t.Fatalf("cap = %v, %v; want 5, nil", cap, err)
	}
}

func TestWorkLimitMeterToolBoundaries(t *testing.T) {
	for _, limit := range []int{4} {
		t.Run("limit", func(t *testing.T) {
			meter := workLimitMeter{limits: runtime.WorkLimits{MaxToolCalls: limit}}
			if err := meter.reserveToolBatch(limit - 1); err != nil {
				t.Fatalf("max-1 reservation: %v", err)
			}
			if err := meter.reserveToolBatch(1); err != nil {
				t.Fatalf("max reservation: %v", err)
			}
			if err := meter.reserveToolBatch(1); err == nil {
				t.Fatal("max+1 reservation succeeded")
			}
		})
	}
}

func TestWorkLimitMeterOneAllowsExactlyOneTool(t *testing.T) {
	meter := workLimitMeter{limits: runtime.WorkLimits{MaxToolCalls: 1}}
	if err := meter.reserveToolBatch(1); err != nil {
		t.Fatal(err)
	}
	if err := meter.reserveToolBatch(1); err == nil {
		t.Fatal("second tool exceeded one-call limit")
	}
}

// reservePromptOnly charges prompt tokens only and never touches the output
// allowance. It backs recovery paths (prompt-too-long compaction retry) where
// the prompt is genuinely new work but the completion's output was already
// reserved by the rejected attempt: charging output twice for one logical
// completion would hard-fail a finite MaxOutputTokens budget.
func TestWorkLimitMeterReservePromptOnlyChargesPromptNotOutput(t *testing.T) {
	// (a) zero limits are unlimited, and a prompt-only reserve never charges output.
	var meter workLimitMeter
	if err := meter.reservePromptOnly(1 << 20); err != nil {
		t.Fatalf("unlimited reservePromptOnly: %v", err)
	}
	if meter.promptTokens != 1<<20 {
		t.Fatalf("promptTokens = %d, want %d", meter.promptTokens, 1<<20)
	}
	if meter.outputTokens != 0 {
		t.Fatalf("outputTokens = %d, want 0 (prompt-only reserve must not charge output)", meter.outputTokens)
	}

	// (b) finite MaxPromptTokens: the cumulative prompt bound is respected.
	meter = workLimitMeter{limits: runtime.WorkLimits{MaxPromptTokens: 5}}
	if err := meter.reservePromptOnly(3); err != nil {
		t.Fatal(err)
	}
	if err := meter.reservePromptOnly(2); err != nil {
		t.Fatal(err)
	}
	if meter.promptTokens != 5 {
		t.Fatalf("promptTokens = %d, want 5", meter.promptTokens)
	}
	if err := meter.reservePromptOnly(1); err == nil {
		t.Fatal("third prompt-only reserve exceeded the cumulative prompt bound")
	}

	// (c) the output path is untouched: after prompt-only reserves, an output
	// reservation still succeeds exactly on the output bound and then rejects.
	meter = workLimitMeter{limits: runtime.WorkLimits{MaxPromptTokens: 5, MaxOutputTokens: 8}}
	if err := meter.reservePromptOnly(4); err != nil {
		t.Fatal(err)
	}
	if err := meter.reserveProvider(0, 8); err != nil {
		t.Fatalf("output reservation after prompt-only reserves: %v", err)
	}
	if meter.outputTokens != 8 {
		t.Fatalf("outputTokens = %d, want 8", meter.outputTokens)
	}
	if err := meter.reserveProvider(0, 1); err == nil {
		t.Fatal("output reservation past the bound succeeded")
	}

	// (d) nil-receiver safety, mirroring reserveProvider.
	if err := (*workLimitMeter)(nil).reservePromptOnly(100); err != nil {
		t.Fatalf("nil receiver reservePromptOnly: %v", err)
	}
}

// refundProvider subtracts a reservation that never completed (a soft steer
// canceled the LLM call before completion, plan 54 §4.3). An exact refund
// restores the balances; an over-refund clamps at zero so an accounting drift
// can never produce a negative budget (a negative balance would widen the next
// outputCap allocation); a nil receiver is a no-op mirroring reserveProvider.
func TestWorkLimitMeterRefundProvider(t *testing.T) {
	// (a) exact refund restores the balances, and the restored budget is spendable.
	meter := workLimitMeter{limits: runtime.WorkLimits{MaxOutputTokens: 8}}
	if err := meter.reserveProvider(0, 4); err != nil {
		t.Fatal(err)
	}
	meter.refundProvider(0, 4)
	if meter.promptTokens != 0 || meter.outputTokens != 0 {
		t.Fatalf("exact refund: prompt=%d output=%d, want 0 0", meter.promptTokens, meter.outputTokens)
	}
	if err := meter.reserveProvider(0, 8); err != nil {
		t.Fatalf("reserve after exact refund: %v", err)
	}
	if meter.outputTokens != 8 {
		t.Fatalf("output after re-reserve = %d, want 8", meter.outputTokens)
	}

	// (b) over-refund clamps at zero (never negative).
	meter = workLimitMeter{}
	meter.refundProvider(100, 100)
	if meter.promptTokens != 0 || meter.outputTokens != 0 {
		t.Fatalf("refund of an empty meter: prompt=%d output=%d, want 0 0", meter.promptTokens, meter.outputTokens)
	}
	meter.reserveProvider(3, 4)
	meter.refundProvider(10, 10)
	if meter.promptTokens != 0 || meter.outputTokens != 0 {
		t.Fatalf("over-refund clamps at zero: prompt=%d output=%d, want 0 0", meter.promptTokens, meter.outputTokens)
	}

	// (c) nil receiver is a no-op, mirroring reserveProvider.
	if err := (*workLimitMeter)(nil).reserveProvider(1, 1); err != nil {
		t.Fatalf("nil receiver reserveProvider: %v", err)
	}
	(*workLimitMeter)(nil).refundProvider(1, 1) // must not panic
}
