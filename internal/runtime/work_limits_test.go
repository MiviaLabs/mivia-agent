package runtime

import (
	"testing"
	"time"
)

func TestLowestPositiveWorkLimits(t *testing.T) {
	early := time.Date(2026, 8, 9, 1, 0, 0, 0, time.UTC)
	late := early.Add(time.Hour)
	got := LowestPositiveWorkLimits(
		WorkLimits{MaxTurns: 16, MaxPromptTokens: 20, MaxOutputTokens: 30, MaxOutputPerCall: 40, MaxToolCalls: 50, DeadlineAt: late},
		WorkLimits{MaxTurns: 0, MaxPromptTokens: 19, MaxOutputTokens: 0, MaxOutputPerCall: 41, MaxToolCalls: 49, DeadlineAt: early},
	)
	want := WorkLimits{MaxTurns: 16, MaxPromptTokens: 19, MaxOutputTokens: 30, MaxOutputPerCall: 40, MaxToolCalls: 49, DeadlineAt: early}
	if got != want {
		t.Fatalf("limits = %+v, want %+v", got, want)
	}
}

func TestLowestPositiveWorkLimitsZeroIsUnlimited(t *testing.T) {
	if got := LowestPositiveWorkLimits(WorkLimits{}); got != (WorkLimits{}) {
		t.Fatalf("zero limits = %+v, want unlimited zero value", got)
	}
}
