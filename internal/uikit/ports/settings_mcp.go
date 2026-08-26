package ports

import "context"

// MCPState is what is known about a configured server's live
// connection. internal/mcp has no per-server status API beyond
// Failures() (an error map), so MCPStateUnknown is the honest default
// until an adapter grows one.
type MCPState int

const (
	MCPStateUnknown MCPState = iota
	MCPStateConnected
	MCPStateFailed
	MCPStateDisabled
)

// MCPFailKind classifies a connection failure without echoing the
// transport's raw error text, which can carry a URL's query string or
// a header value.
type MCPFailKind int

const (
	MCPFailNone MCPFailKind = iota
	MCPFailSpawn
	MCPFailConnect
	MCPFailTLS
	MCPFailTimeout
	MCPFailProtocol
	MCPFailAuth
)

// MCPServerView is one configured MCP server.
//
// Endpoint is scheme://host/path ONLY - internal/config.MCPServerConfig.URL
// can carry userinfo and a query string ("https://u:p@host?api_key=..."),
// so the full URL is never projected here; it splits at this boundary,
// not at render time.
//
// Args and Command MAY still contain a secret ("npx -y srv --token=...").
// They are not masked here because the editor needs the true value to
// edit it - the mask is a render-time concern in internal/ui - but any
// caller that logs, prints, or writes these to a golden file without
// masking first leaks a secret.
type MCPServerView struct {
	ID             string
	Transport      string
	Command        string
	Args           []string
	Endpoint       string
	EnvNames       []string
	HeaderEnvNames map[string]string
	Enabled        bool
	Global         bool
	TimeoutSeconds int
	State          MCPState
	FailKind       MCPFailKind
	FailMessage    string
	ToolCount      int
	Scope          Scope
	// OriginLabel names the config file this server came from, ready to
	// render. The adapter builds it because only that layer may resolve the
	// namespace directory (internal/workspace); a screen that spelled the
	// path itself would both duplicate the name and cross a layer boundary.
	// Empty for a view a screen builds for itself, such as a new-server
	// editor - see MCPOriginFallback.
	OriginLabel string
}

// MCPOriginFallback names the origin of a server view that carries no
// OriginLabel, so a screen never renders an empty origin.
func MCPOriginFallback(scope Scope, global bool) string {
	if scope == ScopeUser || global {
		return "Global (user)"
	}
	return "Project (workspace)"
}

// MCPEdit is a closed union of MCP server mutations.
type MCPEdit interface{ isMCPEdit() }

type UpsertMCPServer struct{ Server MCPServerView }
type RemoveMCPServer struct{ ID string }
type SetMCPServerEnabled struct {
	ID string
	On bool
}

func (UpsertMCPServer) isMCPEdit()     {}
func (RemoveMCPServer) isMCPEdit()     {}
func (SetMCPServerEnabled) isMCPEdit() {}

// MCPSettings is the MCP section's read/write surface. internal/mcp has
// no runtime enable/disable/restart API today, so SetMCPServerEnabled
// is honoured by the fake and is the real adapter's first job to grow.
type MCPSettings interface {
	MCPServers() []MCPServerView
	Apply(ctx context.Context, scope Scope, e MCPEdit) (SaveHandle, error)
}
