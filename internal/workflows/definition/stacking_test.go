package definition

import (
	"strings"
	"testing"
)

func boolPtr(b bool) *bool { return &b }

func TestStackingEnabled(t *testing.T) {
	tests := []struct {
		name string
		s    *Stacking
		want bool
	}{
		{"nil section defaults to enabled", nil, true},
		{"explicit true", &Stacking{Enabled: boolPtr(true)}, true},
		{"explicit false opts out", &Stacking{Enabled: boolPtr(false)}, false},
		{"absent enabled field defaults to enabled", &Stacking{MaxChunks: 6}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.s.StackingEnabled(); got != tt.want {
				t.Errorf("StackingEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEffectiveStacking(t *testing.T) {
	t.Run("nil section yields global defaults", func(t *testing.T) {
		var s *Stacking
		cfg := s.EffectiveStacking("plan", "implement")
		want := StackingConfig{
			Enabled:       true,
			PlanStep:      "plan",
			ImplementStep: "implement",
			MaxChunks:     DefaultStackingMaxChunks,
			SoftLines:     DefaultStackingSoftLines,
			HardLines:     DefaultStackingHardLines,
			MaxFiles:      DefaultStackingMaxFiles,
			MergePolicy:   DefaultStackingMergePolicy,
		}
		if cfg != want {
			t.Errorf("EffectiveStacking() = %+v, want %+v", cfg, want)
		}
	})

	t.Run("explicit values override, defaults fill the rest", func(t *testing.T) {
		s := &Stacking{
			Enabled:       boolPtr(true),
			PlanStep:      "fix_plan",
			ImplementStep: "implement",
			MaxChunks:     6,
			HardLines:     300,
			MergePolicy:   "auto",
			Agent:         "workflow-engineer",
		}
		cfg := s.EffectiveStacking("fix_plan", "implement")
		if cfg.MaxChunks != 6 || cfg.SoftLines != DefaultStackingSoftLines || cfg.HardLines != 300 {
			t.Errorf("resolved thresholds wrong: %+v", cfg)
		}
		if cfg.MergePolicy != "auto" || cfg.Agent != "workflow-engineer" {
			t.Errorf("resolved policy/agent wrong: %+v", cfg)
		}
		if cfg.PlanStep != "fix_plan" || cfg.ImplementStep != "implement" {
			t.Errorf("resolved steps wrong: %+v", cfg)
		}
	})

	t.Run("disabled section still resolves, flagged off", func(t *testing.T) {
		s := &Stacking{Enabled: boolPtr(false), MaxChunks: 3}
		cfg := s.EffectiveStacking("", "")
		if cfg.Enabled {
			t.Error("cfg.Enabled = true, want false")
		}
		if cfg.MaxChunks != 3 {
			t.Errorf("cfg.MaxChunks = %d, want 3", cfg.MaxChunks)
		}
	})
}

func TestParseWorkflowTOML_Stacking(t *testing.T) {
	tomlDoc := `
version = 1
name = "stacked-workflow"
initial_step = "plan"

[stacking]
enabled = true
plan_step = "plan"
implement_step = "implement"
max_chunks = 8
soft_lines = 150
hard_lines = 250
max_files = 3
merge_policy = "auto"
agent = "workflow-engineer"

[inputs]
task = { type = "string", required = true }

[[steps]]
id = "plan"
kind = "agent"
agent = "workflow-engineer"

[[steps]]
id = "implement"
kind = "agent"
agent = "workflow-engineer"

[[transitions]]
from = "plan"
to = "implement"
match = { status = "succeeded" }

[[transitions]]
from = "implement"
to = "success"
match = { status = "succeeded" }
`
	wf, _, err := ParseWorkflowTOML([]byte(tomlDoc), "stacked-workflow.toml")
	if err != nil {
		t.Fatalf("ParseWorkflowTOML failed: %v", err)
	}
	if wf.Stacking == nil {
		t.Fatal("Stacking section not decoded")
	}
	s := wf.Stacking
	if s.Enabled == nil || !*s.Enabled {
		t.Error("enabled = true not decoded")
	}
	if s.PlanStep != "plan" || s.ImplementStep != "implement" {
		t.Errorf("steps not decoded: plan=%q implement=%q", s.PlanStep, s.ImplementStep)
	}
	if s.MaxChunks != 8 || s.SoftLines != 150 || s.HardLines != 250 || s.MaxFiles != 3 {
		t.Errorf("thresholds not decoded: %+v", s)
	}
	if s.MergePolicy != "auto" || s.Agent != "workflow-engineer" {
		t.Errorf("policy/agent not decoded: %+v", s)
	}
}

func TestParseWorkflowTOML_StackingUnknownKey(t *testing.T) {
	tomlDoc := `
version = 1
name = "wf"
initial_step = "plan"

[stacking]
bogus_key = 1

[[steps]]
id = "plan"
kind = "agent"
agent = "workflow-engineer"
`
	_, _, err := ParseWorkflowTOML([]byte(tomlDoc), "wf.toml")
	if err == nil {
		t.Fatal("expected decode error for unknown [stacking] key, got nil")
	}
	if !strings.Contains(err.Error(), "strict mode") {
		t.Errorf("error %q should mention strict-mode rejection", err.Error())
	}
}
