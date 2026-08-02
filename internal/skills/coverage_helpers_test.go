package skills

import "testing"

func TestModelFacingAndActivationHelpers(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(Definition{Name: "alpha", Description: "first"}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(Definition{Name: "beta"}); err != nil {
		t.Fatal(err)
	}
	allow := []string{"beta"}
	if got := registry.ListModelFacing(&allow); len(got) != 1 || got[0].Name != "beta" || got[0].Display != "beta" {
		t.Fatalf("allowlisted model skills = %#v", got)
	}
	empty := []string{}
	if got := registry.ListModelFacing(&empty); got != nil {
		t.Fatalf("empty allowlist = %#v, want nil", got)
	}
	if got := registry.ListModelFacing(nil); len(got) != 2 || got[0].Display != "alpha - first" {
		t.Fatalf("all model skills = %#v", got)
	}
	activation := &SkillActivation{definition: Definition{Instructions: "base", Resources: []ResourceDescriptor{{ID: "guide", Summary: "usage"}}}, resources: map[string]ResourceDescriptor{"guide": {}}, key: "opaque-key"}
	if got := activation.Prompt(true); got != "base\n\n<skill-resources>\n- id: guide\n  purpose: usage\n</skill-resources>" {
		t.Fatalf("resource prompt = %q", got)
	}
	if activation.ToolKey() != "opaque-key" || activation.ToolResultBudget() != resourceToolResultBytes {
		t.Fatal("activation tool helpers returned unexpected metadata")
	}
	bare := &SkillActivation{definition: Definition{Instructions: "base"}}
	if bare.Prompt(true) != "base" || bare.ToolKey() != "" {
		t.Fatal("activation without resources returned unexpected metadata")
	}
	var nilActivation *SkillActivation
	if nilActivation.Prompt(true) != "" || nilActivation.ToolKey() != "" {
		t.Fatal("nil activation returned metadata")
	}
}
