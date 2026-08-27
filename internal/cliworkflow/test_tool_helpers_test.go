package cliworkflow

// test_tool_helpers_test.go duplicates cli's namedTool test stub
// (agent_integration_test.go): a minimal tools.Tool with a fixed name.

import (
	"context"
	"encoding/json"
)

type namedTool struct{ name string }

func (t namedTool) Name() string               { return t.name }
func (t namedTool) Description() string        { return t.name }
func (t namedTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (t namedTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "ok", nil
}
