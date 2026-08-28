package clichat

// Budget tests for the whole-subagent wall clock. totalTaskTimeout resolves
// [subagents] default_total_timeout_seconds; the incident behind it was a
// trickling provider connection that kept a subagent "running" for over ten
// minutes past its final report, because no construction site set a total
// budget and idle watchdogs only see silence, not a slow drip.

import (
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/cliorchestrate"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
)

// TestTotalTaskTimeout pins the resolution table for
// default_total_timeout_seconds: unset falls to the compiled 60-minute
// default, a positive value is the budget, and a negative value switches
// the bound off (the documented operator opt-out).
func TestTotalTaskTimeout(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		configured int
		want       time.Duration
	}{
		{name: "unset_uses_compiled_default", configured: 0, want: config.DefaultSubagentTotalTimeoutSec * time.Second},
		{name: "positive_is_the_budget", configured: 90, want: 90 * time.Second},
		{name: "negative_switches_off", configured: -1, want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := totalTaskTimeout(tc.configured); got != tc.want {
				t.Fatalf("totalTaskTimeout(%d) = %v, want %v", tc.configured, got, tc.want)
			}
		})
	}
}

// TestSkillHandlerCarriesTotalBudget checks the resolved budget on the
// skill surface's constructed handler (field assertion, not wall time),
// so the same knob that bounds routed agents also bounds skill runs.
func TestSkillHandlerCarriesTotalBudget(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		configured int
		want       time.Duration
	}{
		{name: "unset_uses_compiled_default", configured: 0, want: config.DefaultSubagentTotalTimeoutSec * time.Second},
		{name: "positive_is_the_budget", configured: 45, want: 45 * time.Second},
		{name: "negative_switches_off", configured: -1, want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.SubagentConfig{DefaultTotalTimeoutSec: tc.configured}
			h := newSkillMultiStepHandler(skillHandlerDeps{}, cfg, skills.Definition{Name: "review"})
			if h.TotalTimeout != tc.want {
				t.Fatalf("skill handler TotalTimeout = %v, want %v", h.TotalTimeout, tc.want)
			}
		})
	}
}

// TestSkillHandlerMaxTokensOverride pins the caller-supplied max-tokens
// override: a positive pointer wins over the compiled default, a nil
// pointer or a non-positive value leaves the default in place.
func TestSkillHandlerMaxTokensOverride(t *testing.T) {
	t.Parallel()
	positive := 777
	nonPositive := 0
	cases := []struct {
		name      string
		maxTokens *int
		want      int
	}{
		{name: "nil_pointer_keeps_default", maxTokens: nil, want: cliorchestrate.DefaultMaxTokens},
		{name: "positive_pointer_wins", maxTokens: &positive, want: 777},
		{name: "non_positive_pointer_keeps_default", maxTokens: &nonPositive, want: cliorchestrate.DefaultMaxTokens},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newSkillMultiStepHandler(skillHandlerDeps{maxTokens: tc.maxTokens},
				config.SubagentConfig{}, skills.Definition{Name: "review"})
			if h.MaxTokens != tc.want {
				t.Fatalf("skill handler MaxTokens = %d, want %d", h.MaxTokens, tc.want)
			}
		})
	}
}
