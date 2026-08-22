package cli

// load_tools moved to internal/cliagents. This file pins the Capability class
// from the cli package (where the tool is registered). The type is in
// cliagents; registration remains here in internal/cli/dispatcher.go.

import (
	"encoding/json"
	"testing"

	cliagents "github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// TestLoadToolsDeclaredExecutionWrite pins the load_tools Capability class.
func TestLoadToolsDeclaredExecutionWrite(t *testing.T) {
	tool := cliagents.NewLoadToolsTool(nil, nil)
	capable, ok := tool.(tools.CapableTool)
	if !ok {
		t.Fatal("load_tools does not implement tools.CapableTool")
	}
	if got := capable.Capability(json.RawMessage(`{}`)).Class; got != tools.ExecutionWrite {
		t.Fatalf("load_tools Capability class = %v, want ExecutionWrite", got)
	}
}
