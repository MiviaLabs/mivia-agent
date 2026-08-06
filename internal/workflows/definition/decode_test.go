package definition

import (
	"os"
	"strings"
	"testing"
)

func TestParseWorkflowTOML_ValidFixture(t *testing.T) {
	data, err := os.ReadFile("../testdata/valid-feature-delivery.toml")
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	wf, name, err := ParseWorkflowTOML(data, "feature-delivery.toml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "feature-delivery" {
		t.Errorf("name = %q, want %q", name, "feature-delivery")
	}
	if wf.Name != name {
		t.Errorf("wf.Name = %q, want %q", wf.Name, name)
	}
	if wf.Version != 1 {
		t.Errorf("version = %d, want 1", wf.Version)
	}
	if wf.InitialStep != "plan" {
		t.Errorf("initial_step = %q, want %q", wf.InitialStep, "plan")
	}
	if len(wf.Steps) != 9 {
		t.Errorf("len(steps) = %d, want 9", len(wf.Steps))
	}
	if len(wf.Transitions) != 12 {
		t.Errorf("len(transitions) = %d, want 12", len(wf.Transitions))
	}
	if wf.Delivery == nil {
		t.Error("delivery is nil, want non-nil")
	}
}

func TestParseWorkflowTOML_AgentStepSkill(t *testing.T) {
	data := []byte(`
version = 1
name = "skill-binding"
initial_step = "plan"

[[steps]]
id = "plan"
kind = "agent"
agent = "worker"
skill = "workflow-delivery"

[[transitions]]
from = "plan"
to = "success"
match = { status = "succeeded" }
`)
	wf, _, err := ParseWorkflowTOML(data, "skill-binding.toml")
	if err != nil {
		t.Fatal(err)
	}
	if got := wf.Steps[0].Skill; got != "workflow-delivery" {
		t.Fatalf("skill = %q, want workflow-delivery", got)
	}
}

func TestParseWorkflowTOML_UnknownField(t *testing.T) {
	data, err := os.ReadFile("../testdata/invalid/unknown-field.toml")
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	_, _, err = ParseWorkflowTOML(data, "unknown-field.toml")
	if err == nil {
		t.Fatal("expected error for unknown field, got nil")
	}
	// go-toml/v2 reports unknown fields with a specific message.
	if !strings.Contains(err.Error(), "unknown field") && !strings.Contains(err.Error(), "missing in the target struct") {
		t.Errorf("error %q should mention unknown field", err.Error())
	}
}

func TestParseWorkflowTOML_EmptyStepID(t *testing.T) {
	data, err := os.ReadFile("../testdata/invalid/empty-step-id.toml")
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	_, _, err = ParseWorkflowTOML(data, "empty-step-id.toml")
	if err == nil {
		t.Fatal("expected error for empty step ID, got nil")
	}
	// The initial_step is empty, so that should be caught first.
	// But the step also has empty id. Both should produce an error.
	if !strings.Contains(err.Error(), "initial_step") && !strings.Contains(err.Error(), "id is required") {
		t.Errorf("error %q should mention initial_step or step id", err.Error())
	}
}

func TestParseWorkflowTOML_ReservedStepID(t *testing.T) {
	data, err := os.ReadFile("../testdata/invalid/reserved-step-id.toml")
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	_, _, err = ParseWorkflowTOML(data, "reserved-step-id.toml")
	if err == nil {
		t.Fatal("expected error for reserved step ID, got nil")
	}
	if !strings.Contains(err.Error(), "reserved") {
		t.Errorf("error %q should mention reserved", err.Error())
	}
}

func TestParseWorkflowTOML_NameMismatch(t *testing.T) {
	data := []byte(`
version = 1
name = "other-name"
description = "Test"
initial_step = "plan"

[[steps]]
id = "plan"
kind = "human_gate"

[[transitions]]
from = "plan"
to = "success"
match = { status = "approved" }
`)
	_, _, err := ParseWorkflowTOML(data, "my-workflow.toml")
	if err == nil {
		t.Fatal("expected error for name mismatch, got nil")
	}
	if !strings.Contains(err.Error(), "does not match filename") {
		t.Errorf("error %q should mention filename mismatch", err.Error())
	}
}

func TestParseWorkflowTOML_EmptyName(t *testing.T) {
	data := []byte(`
version = 1
name = ""
description = "Test"
initial_step = "plan"

[[steps]]
id = "plan"
kind = "human_gate"

[[transitions]]
from = "plan"
to = "success"
match = { status = "approved" }
`)
	_, _, err := ParseWorkflowTOML(data, "my-workflow.toml")
	if err == nil {
		t.Fatal("expected error for empty name, got nil")
	}
	if !strings.Contains(err.Error(), "name is required") {
		t.Errorf("error %q should mention name is required", err.Error())
	}
}

func TestParseWorkflowTOML_UnsupportedVersion(t *testing.T) {
	data := []byte(`
version = 2
name = "my-workflow"
description = "Test"
initial_step = "plan"

[[steps]]
id = "plan"
kind = "human_gate"

[[transitions]]
from = "plan"
to = "success"
match = { status = "approved" }
`)
	_, _, err := ParseWorkflowTOML(data, "my-workflow.toml")
	if err == nil {
		t.Fatal("expected error for unsupported version, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported version") {
		t.Errorf("error %q should mention unsupported version", err.Error())
	}
}

func TestParseWorkflowTOML_NegativeInputMaxBytes(t *testing.T) {
	data, err := os.ReadFile("../testdata/invalid/negative-input-max-bytes.toml")
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	_, _, err = ParseWorkflowTOML(data, "negative-input-max-bytes.toml")
	if err == nil {
		t.Fatal("expected error for negative max_bytes, got nil")
	}
	if !strings.Contains(err.Error(), "max_bytes") {
		t.Errorf("error %q should mention max_bytes", err.Error())
	}
}

// assertParseError parses body with filename and verifies the error contains substr.
func assertParseError(t *testing.T, body, filename, substr string) {
	t.Helper()
	_, _, err := ParseWorkflowTOML([]byte(body), filename)
	if err == nil {
		t.Fatalf("expected parse error for %q, got nil", filename)
	}
	if !strings.Contains(err.Error(), substr) {
		t.Errorf("error %q should contain %q", err.Error(), substr)
	}
}

func TestParseWorkflowTOML_FilenameValidation(t *testing.T) {
	tests := []struct{ name, filename, substr string }{
		{"not toml suffix", "notes.txt", "must end in .toml"},
		{"empty name", ".toml", "workflow name is empty"},
		{"invalid name characters", "weird:name.toml", `workflow name "weird:name" is invalid`},
		{"uppercase name", "Bad.toml", "must be lowercase"},
		{"invalid name char", "my.workflow.toml", "contains invalid character"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := ParseWorkflowTOML(nil, tc.filename)
			if err == nil {
				t.Fatalf("expected error for filename %q, got nil", tc.filename)
			}
			if !strings.Contains(err.Error(), tc.substr) {
				t.Errorf("error %q should contain %q", err.Error(), tc.substr)
			}
		})
	}
}

func TestParseWorkflowTOML_StepValidation(t *testing.T) {
	base := func(step string) string {
		return "version = 1\nname = \"step-validation\"\ninitial_step = \"plan\"\n\n" +
			step + "\n\n[[transitions]]\nfrom = \"plan\"\nto = \"success\"\nmatch = { status = \"succeeded\" }\n"
	}
	tests := []struct{ name, step, substr string }{
		{"empty step id", "[[steps]]\nid = \"\"\nkind = \"agent\"\nagent = \"planner\"", "id is required"},
		{"reserved step id", "[[steps]]\nid = \"success\"\nkind = \"agent\"\nagent = \"planner\"", "reserved"},
		{"unknown step kind", "[[steps]]\nid = \"plan\"\nkind = \"bogus\"", "unknown kind"},
		{"agent without agent field", "[[steps]]\nid = \"plan\"\nkind = \"agent\"", "agent is required"},
		{"evidence gate without verifier", "[[steps]]\nid = \"plan\"\nkind = \"evidence_gate\"", "verifier is required"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertParseError(t, base(tc.step), "step-validation.toml", tc.substr)
		})
	}
}

func TestParseWorkflowTOML_DuplicateStepID(t *testing.T) {
	body := `version = 1
name = "dup-step"
initial_step = "plan"

[[steps]]
id = "plan"
kind = "human_gate"

[[steps]]
id = "plan"
kind = "human_gate"

[[transitions]]
from = "plan"
to = "success"
match = { status = "approved" }
`
	assertParseError(t, body, "dup-step.toml", `duplicate step ID "plan"`)
}

func TestParseWorkflowTOML_TransitionValidation(t *testing.T) {
	base := func(transition string) string {
		return "version = 1\nname = \"transition-validation\"\ninitial_step = \"plan\"\n\n" +
			"[[steps]]\nid = \"plan\"\nkind = \"human_gate\"\n\n" + transition + "\n"
	}
	tests := []struct{ name, transition, substr string }{
		{"missing from", "[[transitions]]\nfrom = \"\"\nto = \"success\"\nmatch = { status = \"approved\" }", "from is required"},
		{"missing to", "[[transitions]]\nfrom = \"plan\"\nto = \"\"\nmatch = { status = \"approved\" }", "to is required"},
		{"unknown from", "[[transitions]]\nfrom = \"start\"\nto = \"success\"\nmatch = { status = \"approved\" }", `from "start" is not a declared step`},
		{"unknown to", "[[transitions]]\nfrom = \"plan\"\nto = \"finish\"\nmatch = { status = \"approved\" }", `to "finish" is not a declared step or terminal`},
		{"missing match status", "[[transitions]]\nfrom = \"plan\"\nto = \"success\"\nmatch = {}", "match.status is required"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertParseError(t, base(tc.transition), "transition-validation.toml", tc.substr)
		})
	}
}

func TestParseWorkflowTOML_InputValidation(t *testing.T) {
	base := func(inputs string) string {
		return "version = 1\nname = \"input-validation\"\ninitial_step = \"plan\"\n\n" +
			inputs + "\n\n[[steps]]\nid = \"plan\"\nkind = \"human_gate\"\n\n" +
			"[[transitions]]\nfrom = \"plan\"\nto = \"success\"\nmatch = { status = \"approved\" }\n"
	}
	tests := []struct{ name, inputs, substr string }{
		{"missing type", "[inputs]\ntitle = { type = \"\" }", "type is required"},
		{"max bytes too large", "[inputs]\ndata = { type = \"string\", max_bytes = 2000000 }", "max_bytes"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertParseError(t, base(tc.inputs), "input-validation.toml", tc.substr)
		})
	}
}
