package tools

import "encoding/json"

func (t *searchTool) Capability(args json.RawMessage) Capability {
	var input struct {
		Scope string `json:"scope"`
	}
	_ = json.Unmarshal(args, &input)
	if input.Scope == "local" {
		return Capability{Class: ExecutionRead, ResourceKey: "workspace:read"}
	}
	return Capability{Class: ExecutionExternal}
}
