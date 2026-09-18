package cli

import (
	"strings"
	"testing"
)

// Coverage repair: runConfigShow's --config flag parse error path.
func TestRunConfigRejectsFlagWithoutValue(t *testing.T) {
	err := runConfig([]string{"show", "--config"})
	if err == nil || !strings.Contains(err.Error(), "requires a value") {
		t.Fatalf("runConfig(show --config) error = %v, want requires-a-value", err)
	}
	err = runConfig([]string{"bogus"})
	if err == nil || !strings.Contains(err.Error(), "unknown config subcommand") {
		t.Fatalf("runConfig(bogus) error = %v, want unknown-subcommand", err)
	}
}
