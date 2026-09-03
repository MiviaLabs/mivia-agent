package mcp

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// discoveredTool must satisfy tools.ExternalOrigin, or the registry reports
// every server tool as compiled in and the sidebar's server row reads zero
// forever. The interface is satisfied structurally, so without this assertion
// dropping the method fails nothing.
var _ tools.ExternalOrigin = discoveredTool{}

// TestDiscoveredToolReportsItsServer pins the value, not just the method set:
// a method returning "" would satisfy the interface and still leave every
// server tool anonymous.
func TestDiscoveredToolReportsItsServer(t *testing.T) {
	tool := discoveredTool{name: "linear_issue", serverID: "linear"}
	if got := tool.OriginServer(); got != "linear" {
		t.Errorf("OriginServer() = %q, want \"linear\"", got)
	}
}
