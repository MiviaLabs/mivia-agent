package tools

import "encoding/json"

func (t *webSearchTool) Capability(args json.RawMessage) Capability {
	return Capability{Class: ExecutionExternal}
}
