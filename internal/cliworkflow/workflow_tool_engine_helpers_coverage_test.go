package cliworkflow

// workflow_tool_engine_helpers_coverage_test.go covers the small pure
// helpers in workflow_tool_engine.go and workflow_tool_engine_ops.go
// that the broad engine_*_test.go files do not exercise individually.

import (
	"strings"
	"testing"
)

func TestInputsToRawFlags(t *testing.T) {
	flags, err := inputsToRawFlags(map[string]any{
		"workspace_root": "/tmp/ws",
		"key":            "value with space",
		"port":           8080,
	})
	if err != nil {
		t.Fatalf("inputsToRawFlags: %v", err)
	}
	joined := strings.Join(flags, " ")
	for _, want := range []string{"workspace_root=", "/tmp/ws", "key=", "value with space", "port=", "8080"} {
		if !strings.Contains(joined, want) {
			t.Errorf("inputsToRawFlags missing %q in %q", want, joined)
		}
	}
	// Empty map produces no flags.
	if flags, err := inputsToRawFlags(nil); err != nil || len(flags) != 0 {
		t.Fatalf("inputsToRawFlags(nil) = (%v, %v)", flags, err)
	}
}

func TestInputsToRawFlagsSkipsUnparseableValues(t *testing.T) {
	// A value that cannot be rendered as a flag argument must error.
	// Functions are not representable in TOML/JSON, so a function value
	// forces the encoder to fail.
	if _, err := inputsToRawFlags(map[string]any{"k": func() {}}); err == nil {
		t.Fatal("inputsToRawFlags(func value) must error")
	}
}
