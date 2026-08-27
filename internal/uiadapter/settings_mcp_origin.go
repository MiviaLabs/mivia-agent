package uiadapter

import (
	"path"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// mcpConfigFile is the config file name inside the namespace directory. The
// directory itself comes from workspace.Namespace, which is the only place
// allowed to name it (see TestNamespaceNameSingleSourced).
const mcpConfigFile = "mivia.toml"

// mcpOriginLabel renders the config file an MCP server was declared in.
// Built here because internal/uiadapter is the layer permitted to import
// internal/workspace; the settings screen only renders the result.
//
// path.Join, not filepath.Join: this is a label, so it must read with forward
// slashes on every platform rather than following the host separator.
func mcpOriginLabel(scope ports.Scope, global bool) string {
	file := path.Join(workspace.Namespace, mcpConfigFile)
	if scope == ports.ScopeUser || global {
		return "Global (user: ~/" + file + ")"
	}
	return "Project (workspace: " + file + ")"
}
