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
	if _, err := meter.outputCap(nil); err == nil {
		t.Fatal("partial final output allocation was accepted")
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
