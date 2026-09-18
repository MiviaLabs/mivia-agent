package workflow

// Test helpers shared by tests that parse the committed .mivia/workflows
// definitions. Mirrors chat's feature_delivery_contract_test.go helpers.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

func committedWorkflowRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".mivia", "workflows", "feature-delivery.toml")); err != nil {
		t.Skipf("committed feature-delivery workflow is not present: %v", err)
	}
	return root
}

func loadCommittedFeatureDeliveryWorkflow(t *testing.T, root string) (definition.WorkflowFile, string) {
	t.Helper()
	base := filepath.Join(root, ".mivia", "workflows")
	raw, err := os.ReadFile(filepath.Join(base, "feature-delivery.toml"))
	if err != nil {
		t.Fatal(err)
	}
	workflow, _, err := definition.ParseWorkflowTOML(raw, "feature-delivery.toml")
	if err != nil {
		t.Fatalf("parse committed feature-delivery workflow: %v", err)
	}
	return workflow, base
}

func featureDeliveryStep(t *testing.T, workflow definition.WorkflowFile, id string) definition.Step {
	t.Helper()
	for _, step := range workflow.Steps {
		if step.ID == id {
			return step
		}
	}
	t.Fatalf("feature-delivery step %q is missing", id)
	return definition.Step{}
}
