package cli

import (
	"strings"
	"testing"
)

func TestSliceErrorsNilAndEmpty(t *testing.T) {
	if err := sliceErrors("workflow", nil); err != nil {
		t.Fatalf("sliceErrors(nil) = %v, want nil", err)
	}
	if err := sliceErrors("workflow", []string{}); err != nil {
		t.Fatalf("sliceErrors(empty) = %v, want nil", err)
	}
}

func TestSliceErrorsJoinsMessages(t *testing.T) {
	err := sliceErrors("workflow", []string{"first problem", "second problem"})
	if err == nil {
		t.Fatal("sliceErrors(non-empty) = nil, want error")
	}
	want := "workflow: first problem; second problem"
	if err.Error() != want {
		t.Errorf("sliceErrors error = %q, want %q", err.Error(), want)
	}
	if !strings.Contains(err.Error(), "first problem") || !strings.Contains(err.Error(), "second problem") {
		t.Errorf("sliceErrors error %q must contain every message", err.Error())
	}
}
