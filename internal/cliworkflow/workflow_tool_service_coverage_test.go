package cliworkflow

import (
	"testing"
)

// TestWireWorkflowToolOptionsNilOptionsNoop covers the nil-options guard in
// WireWorkflowToolOptions: with opts nil the helper must return before any
// service build, even when the workspace and provider are supplied.
func TestWireWorkflowToolOptionsNilOptionsNoop(t *testing.T) {
	root := t.TempDir()
	WireWorkflowToolOptions(nil, root, nil, nil, false)
}
