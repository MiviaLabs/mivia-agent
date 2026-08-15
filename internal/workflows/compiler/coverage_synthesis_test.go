package compiler

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

func TestCoverageSynthesizeStackingFailClosed(t *testing.T) {
	for _, tt := range []struct {
		name    string
		apply   func(*definition.WorkflowFile)
		wantErr string
	}{
		{"plan step with no agent and empty stacking.agent", func(w *definition.WorkflowFile) { w.Steps[0].Agent = "" }, "no agent"},
		{"reserved loop name decompose_repair", func(w *definition.WorkflowFile) {
			w.Transitions = append(w.Transitions, definition.Transition{From: "plan", To: "plan", Match: definition.MatchCriteria{Status: "failed"}, Loop: "decompose_repair", MaxIterations: 3})
		}, "engine-reserved"},
		{"reserved step id chunk_plan_validate", func(w *definition.WorkflowFile) {
			w.Steps = append(w.Steps, definition.Step{ID: "chunk_plan_validate", Kind: "agent", Agent: "workflow-engineer"})
			w.Transitions = append(w.Transitions, definition.Transition{From: "implement", To: "chunk_plan_validate", Match: definition.MatchCriteria{Status: "failed"}}, definition.Transition{From: "chunk_plan_validate", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}})
		}, "engine-reserved"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			wf := stackingFixture()
			tt.apply(wf)
			cw := covCompile(t, wf)
			if cw.Stacking == nil {
				t.Fatal("fixture did not resolve stacking")
			}
			_, err := SynthesizeStacking(cw)
			if err == nil {
				t.Fatal("SynthesizeStacking accepted a reserved or agentless workflow")
			}
			wantSubstrings(t, errToSlice(err), []string{tt.wantErr})
		})
	}
}
func TestCoverageSynthesizeStackingDisabledOptOut(t *testing.T) {
	wf := stackingFixture()
	wf.Stacking = &definition.Stacking{Enabled: boolPtr(false)}
	cw := covCompile(t, wf)
	if cw.Stacking != nil {
		t.Fatalf("CompiledWorkflow.Stacking = %+v, want nil", cw.Stacking)
	}
	synth, err := SynthesizeStacking(cw)
	wantSubstrings(t, errToSlice(err), nil)
	if synth != cw {
		t.Error("SynthesizeStacking returned a different pointer for nil stacking")
	}
	if synth.Digest != cw.Digest {
		t.Errorf("digest changed across synthesis: %q != %q", synth.Digest, cw.Digest)
	}
}
func TestCoverageSynthesizedStepsInheritAgentWithEmptySkill(t *testing.T) {
	wf := stackingFixture()
	wf.Steps[0].Skill = "planning"
	cw := covCompile(t, wf)
	synth, err := SynthesizeStacking(cw)
	wantSubstrings(t, errToSlice(err), nil)
	planAgent := stepByID(t, cw, "plan").Agent
	for _, id := range []string{"decompose", "chunk_plan_validate"} {
		s := stepByID(t, synth, id)
		if s.Agent != planAgent {
			t.Errorf("%s agent = %q, want plan agent %q", id, s.Agent, planAgent)
		}
		if s.Skill != "" {
			t.Errorf("%s skill = %q, want empty (agent-inheritance only)", id, s.Skill)
		}
	}
}
func TestCoverageDigestStableAcrossSynthesis(t *testing.T) {
	cw := compileStackingFixture(t)
	synth, err := SynthesizeStacking(cw)
	wantSubstrings(t, errToSlice(err), nil)
	cw2 := covCompile(t, stackingFixture())
	if cw2.Digest != cw.Digest || synth.Digest != cw.Digest {
		t.Errorf("stacked digest unstable: base %q fresh %q synth %q", cw.Digest, cw2.Digest, synth.Digest)
	}
	plain := covCompile(t, newMinimalWorkflow("synthesis-digest-plain"))
	psynth, err := SynthesizeStacking(plain)
	wantSubstrings(t, errToSlice(err), nil)
	if psynth != plain {
		t.Error("SynthesizeStacking returned a different pointer for nil stacking")
	}
	plain2 := covCompile(t, newMinimalWorkflow("synthesis-digest-plain"))
	if plain2.Digest != plain.Digest {
		t.Errorf("non-stacked digest unstable: %q != %q", plain2.Digest, plain.Digest)
	}
}
