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
