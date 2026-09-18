package cli

import (
	workflow "github.com/MiviaLabs/mivia-agent/internal/cli/workflow"
	"strings"
	"testing"
)

func TestSliceErrorsNilAndEmpty(t *testing.T) {
	if err := workflow.SliceErrors("workflow", nil); err != nil {
		t.Fatalf("workflow.SliceErrors(nil) = %v, want nil", err)
	}
	if err := workflow.SliceErrors("workflow", []string{}); err != nil {
		t.Fatalf("workflow.SliceErrors(empty) = %v, want nil", err)
	}
}

func TestSliceErrorsJoinsMessages(t *testing.T) {
	err := workflow.SliceErrors("workflow", []string{"first problem", "second problem"})
	if err == nil {
		t.Fatal("workflow.SliceErrors(non-empty) = nil, want error")
	}
	want := "workflow: first problem; second problem"
	if err.Error() != want {
		t.Errorf("sliceErrors error = %q, want %q", err.Error(), want)
	}
	if !strings.Contains(err.Error(), "first problem") || !strings.Contains(err.Error(), "second problem") {
		t.Errorf("sliceErrors error %q must contain every message", err.Error())
	}
}
