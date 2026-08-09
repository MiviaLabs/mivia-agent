package cli

import (
	"testing"
)

// TestWireWorkflowToolOptionsNilOptionsNoop covers the nil-options guard in
// wireWorkflowToolOptions: with opts nil the helper must return before any
// service build, even when the workspace and provider are supplied.
func TestWireWorkflowToolOptionsNilOptionsNoop(t *testing.T) {
	root := t.TempDir()
	wireWorkflowToolOptions(nil, root, nil, nil)
}
