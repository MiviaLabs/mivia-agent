package tools

import "encoding/json"

// Capability declares an explicit timeout, shared with fetch_url and
// extract, so search is not invisible to dispatcher timeout policy - see
// http_client.go.
func (t *webSearchTool) Capability(args json.RawMessage) Capability {
	return Capability{Class: ExecutionExternal, Timeout: toolNetworkCapabilityTimeout}
}
