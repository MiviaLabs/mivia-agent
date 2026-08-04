package presentation

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

func TestFormatWorkflowList_Empty(t *testing.T) {
	result := FormatWorkflowList(nil)
	if result != "No workflows found.\n" {
		t.Errorf("expected empty message, got %q", result)
	}
}

func TestFormatWorkflowList_EmptySlice(t *testing.T) {
	result := FormatWorkflowList([]definition.DiscoveredWorkflow{})
	if result != "No workflows found.\n" {
		t.Errorf("expected empty message for empty slice, got %q", result)
	}
}

func TestFormatWorkflowList_Entries(t *testing.T) {
	workflows := []definition.DiscoveredWorkflow{
		{Name: "feature-delivery"},
		{Name: "hotfix"},
	}
	result := FormatWorkflowList(workflows)

	// Should contain count header.
	if result == "" {
		t.Fatal("expected non-empty result")
	}

	// Check count is correct.
	if result != "Workflows (2):\n  feature-delivery\n  hotfix\n" {
		t.Errorf("unexpected output:\n%s", result)
	}
}

func TestFormatWorkflowList_SingleEntry(t *testing.T) {
	workflows := []definition.DiscoveredWorkflow{
		{Name: "feature-delivery", Path: "/tmp/workflows/feature-delivery.toml"},
	}
	result := FormatWorkflowList(workflows)

	if result != "Workflows (1):\n  feature-delivery\n" {
		t.Errorf("unexpected output:\n%s", result)
	}
}
