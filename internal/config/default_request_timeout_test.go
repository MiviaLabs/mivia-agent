package config

import (
	"testing"

	"github.com/pelletier/go-toml/v2"
)

// TestSubagentConfigDefaultRequestTimeoutSec verifies the
// DefaultRequestTimeoutSec field on SubagentConfig.
//
// RED: fails until the field is added to SubagentConfig in types.go.
func TestSubagentConfigDefaultRequestTimeoutSec(t *testing.T) {
	// Present: [subagents] default_request_timeout_seconds = 900 decodes to 900.
	var present File
	if err := toml.Unmarshal([]byte("[subagents]\ndefault_request_timeout_seconds = 900"), &present); err != nil {
		t.Fatalf("parse TOML with field: %v", err)
	}
	if got := present.Subagents.DefaultRequestTimeoutSec; got != 900 {
		t.Fatalf("DefaultRequestTimeoutSec: got %d want 900", got)
	}

	// Omitted: Go zero value (0).
	var absent File
	if err := toml.Unmarshal([]byte("[subagents]\nmax_workers = 8"), &absent); err != nil {
		t.Fatalf("parse TOML without field: %v", err)
	}
	if got := absent.Subagents.DefaultRequestTimeoutSec; got != 0 {
		t.Fatalf("DefaultRequestTimeoutSec (omitted): got %d want 0 (Go zero value)", got)
	}
}
