package config

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
	"github.com/pelletier/go-toml/v2"
)

const (
	defaultMCPStartupTimeoutSeconds = 10
	defaultMCPMaxServers            = 16
	defaultMCPMaxToolsPerServer     = 64
	defaultMCPMaxToolSchemaBytes    = 64 << 10
	defaultMCPMaxToolDescription    = 4 << 10
	defaultMCPMaxToolResultBytes    = 64 << 10
)

// MCPConfigDigest returns a stable, secret-free digest of MCP configuration.
func MCPConfigDigest(cfg MCPConfig) (string, error) {
	payload, err := json.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("marshal MCP configuration: %w", err)
	}
	sum := sha256.Sum256(payload)
	return fmt.Sprintf("sha256:%x", sum[:]), nil
}

// LoadTrustedMCPConfig loads the effective user and project MCP configuration.
// A project server replaces a user server with the same ID as one unit.
func LoadTrustedMCPConfig(workspaceRoot string) (MCPConfig, []string, error) {
	user, err := loadMCPConfigPath(UserConfigPath())
	if err != nil {
		return MCPConfig{}, nil, err
	}
	projectPath := workspace.NamespacePath(workspaceRoot, "mivia.toml")
	if workspaceRoot == "" {
		projectPath = workspace.NamespacePath(".", "mivia.toml")
	}
	if sameFilePath(UserConfigPath(), projectPath) {
		return resolveMCPConfig(user)
	}
	project, err := loadMCPConfigPath(projectPath)
	if err != nil {
		return MCPConfig{}, nil, err
	}
	return resolveMCPConfig(mergeMCPConfig(user, project))
}

func resolveMCPConfig(input mcpConfigInput) (MCPConfig, []string, error) {
	resolved := input.resolve(MCPConfig{})
	if err := validateResolvedMCPConfig(resolved); err != nil {
		return MCPConfig{}, nil, err
	}
	return resolved, nil, nil
}

type mcpConfigInput struct {
	Enabled                 *bool             `toml:"enabled"`
	StartupTimeoutSeconds   *int              `toml:"startup_timeout_seconds"`
	MaxServers              *int              `toml:"max_servers"`
	MaxToolsPerServer       *int              `toml:"max_tools_per_server"`
	MaxToolSchemaBytes      *int              `toml:"max_tool_schema_bytes"`
	MaxToolDescriptionBytes *int              `toml:"max_tool_description_bytes"`
	MaxToolResultBytes      *int              `toml:"max_tool_result_bytes"`
	Servers                 []MCPServerConfig `toml:"servers"`
}

func loadMCPConfigPath(path string) (mcpConfigInput, error) {
	if path == "" {
		return mcpConfigInput{}, nil
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return mcpConfigInput{}, nil
	}
	if err != nil {
		return mcpConfigInput{}, fmt.Errorf("read MCP config %s: %w", path, err)
	}
	if err := validateMCPConfigKeys(data); err != nil {
		return mcpConfigInput{}, fmt.Errorf("MCP config %s: %w", path, err)
	}
	var document struct {
		MCP *mcpConfigInput `toml:"mcp"`
	}
	if err := toml.Unmarshal(data, &document); err != nil {
		return mcpConfigInput{}, fmt.Errorf("parse MCP config %s: %w", path, err)
	}
	if document.MCP == nil {
		return mcpConfigInput{}, nil
	}
	for i := range document.MCP.Servers {
		if err := validateMCPServer(document.MCP.Servers[i]); err != nil {
			return mcpConfigInput{}, fmt.Errorf("MCP config %s: server %d: %w", path, i, err)
		}
	}
	if err := validateMCPServerIDs(document.MCP.Servers); err != nil {
		return mcpConfigInput{}, fmt.Errorf("MCP config %s: %w", path, err)
	}
	return *document.MCP, nil
}

func validateMCPConfigKeys(data []byte) error {
	var document map[string]any
	if err := toml.Unmarshal(data, &document); err != nil {
		return err
	}
	mcpValue, exists := document["mcp"]
	if !exists {
		return nil
	}
	mcpTable, ok := mcpValue.(map[string]any)
	if !ok {
		return fmt.Errorf("[mcp] must be a table")
	}
	if err := validateMCPTableKeys(mcpTable, map[string]struct{}{
		"enabled": {}, "startup_timeout_seconds": {}, "max_servers": {},
		"max_tools_per_server": {}, "max_tool_schema_bytes": {},
		"max_tool_description_bytes": {}, "max_tool_result_bytes": {}, "servers": {},
	}, "[mcp]"); err != nil {
		return err
	}
	servers, exists := mcpTable["servers"]
	if !exists {
		return nil
	}
	serverList, ok := mcpTableSlice(servers)
	if !ok {
		return fmt.Errorf("[mcp].servers must be an array")
	}
	for index, server := range serverList {
		if err := validateMCPTableKeys(server, map[string]struct{}{
			"id": {}, "transport": {}, "command": {}, "url": {}, "args": {}, "env": {},
			"headers": {}, "global": {}, "timeout_seconds": {},
		}, fmt.Sprintf("[mcp].servers[%d]", index)); err != nil {
			return err
		}
		if err := validateMCPHeaders(server["headers"], index); err != nil {
			return err
		}
	}
	return nil
}

func validateMCPTableKeys(values map[string]any, allowed map[string]struct{}, location string) error {
	for key := range values {
		if _, ok := allowed[key]; !ok {
			return fmt.Errorf("unknown key %q in %s", key, location)
		}
	}
	return nil
}

func validateMCPHeaders(value any, serverIndex int) error {
	if value == nil {
		return nil
	}
	headers, ok := mcpTableSlice(value)
	if !ok {
		return fmt.Errorf("[mcp].servers[%d].headers must be an array", serverIndex)
	}
	for index, header := range headers {
		if err := validateMCPTableKeys(header, map[string]struct{}{"name": {}, "value_env": {}}, fmt.Sprintf("[mcp].servers[%d].headers[%d]", serverIndex, index)); err != nil {
			return err
		}
	}
	return nil
}

func mcpTableSlice(value any) ([]map[string]any, bool) {
	switch tables := value.(type) {
	case []map[string]any:
		return tables, true
	case []any:
		out := make([]map[string]any, len(tables))
		for index, table := range tables {
			value, ok := table.(map[string]any)
			if !ok {
				return nil, false
			}
			out[index] = value
		}
		return out, true
	default:
		return nil, false
	}
}

func mergeMCPConfig(user, project mcpConfigInput) mcpConfigInput {
	out := user
	if project.Enabled != nil {
		out.Enabled = project.Enabled
	}
	if project.StartupTimeoutSeconds != nil {
		out.StartupTimeoutSeconds = project.StartupTimeoutSeconds
	}
	if project.MaxServers != nil {
		out.MaxServers = project.MaxServers
	}
	if project.MaxToolsPerServer != nil {
		out.MaxToolsPerServer = project.MaxToolsPerServer
	}
	if project.MaxToolSchemaBytes != nil {
		out.MaxToolSchemaBytes = project.MaxToolSchemaBytes
	}
	if project.MaxToolDescriptionBytes != nil {
		out.MaxToolDescriptionBytes = project.MaxToolDescriptionBytes
	}
	if project.MaxToolResultBytes != nil {
		out.MaxToolResultBytes = project.MaxToolResultBytes
	}
	byID := make(map[string]int, len(out.Servers))
	for i, server := range out.Servers {
		byID[server.ID] = i
	}
	for _, server := range project.Servers {
		if i, exists := byID[server.ID]; exists {
			out.Servers[i] = server
			continue
		}
		byID[server.ID] = len(out.Servers)
		out.Servers = append(out.Servers, server)
	}
	return out
}

func (in mcpConfigInput) resolve(_ MCPConfig) MCPConfig {
	out := MCPConfig{
		StartupTimeoutSeconds:   defaultMCPStartupTimeoutSeconds,
		MaxServers:              defaultMCPMaxServers,
		MaxToolsPerServer:       defaultMCPMaxToolsPerServer,
		MaxToolSchemaBytes:      defaultMCPMaxToolSchemaBytes,
		MaxToolDescriptionBytes: defaultMCPMaxToolDescription,
		MaxToolResultBytes:      defaultMCPMaxToolResultBytes,
		Servers:                 append([]MCPServerConfig(nil), in.Servers...),
	}
	if in.Enabled != nil {
		out.Enabled = *in.Enabled
	}
	if in.StartupTimeoutSeconds != nil {
		out.StartupTimeoutSeconds = *in.StartupTimeoutSeconds
	}
	if in.MaxServers != nil {
		out.MaxServers = *in.MaxServers
	}
	if in.MaxToolsPerServer != nil {
		out.MaxToolsPerServer = *in.MaxToolsPerServer
	}
	if in.MaxToolSchemaBytes != nil {
		out.MaxToolSchemaBytes = *in.MaxToolSchemaBytes
	}
	if in.MaxToolDescriptionBytes != nil {
		out.MaxToolDescriptionBytes = *in.MaxToolDescriptionBytes
	}
	if in.MaxToolResultBytes != nil {
		out.MaxToolResultBytes = *in.MaxToolResultBytes
	}
	return out
}

func validateMCPServer(server MCPServerConfig) error {
	if !validMCPID(server.ID) {
		return fmt.Errorf("id %q is invalid", server.ID)
	}
	if strings.TrimSpace(server.Transport) == "" {
		return fmt.Errorf("transport is required")
	}
	if server.TimeoutSeconds < 0 {
		return fmt.Errorf("timeout_seconds must not be negative")
	}
	for _, arg := range server.Args {
		if strings.ContainsAny(arg, "\x00\r\n") {
			return fmt.Errorf("args must not contain control characters")
		}
	}
	return nil
}

func validateMCPServerIDs(servers []MCPServerConfig) error {
	seen := make(map[string]struct{}, len(servers))
	for _, server := range servers {
		if _, ok := seen[server.ID]; ok {
			return fmt.Errorf("duplicate MCP server ID %q", server.ID)
		}
		seen[server.ID] = struct{}{}
	}
	return nil
}

func validateResolvedMCPConfig(cfg MCPConfig) error {
	if len(cfg.Servers) > cfg.MaxServers {
		return fmt.Errorf("MCP server count %d exceeds max_servers %d", len(cfg.Servers), cfg.MaxServers)
	}
	for _, limit := range []struct {
		name  string
		value int
	}{
		{"startup_timeout_seconds", cfg.StartupTimeoutSeconds},
		{"max_servers", cfg.MaxServers},
		{"max_tools_per_server", cfg.MaxToolsPerServer},
		{"max_tool_schema_bytes", cfg.MaxToolSchemaBytes},
		{"max_tool_description_bytes", cfg.MaxToolDescriptionBytes},
		{"max_tool_result_bytes", cfg.MaxToolResultBytes},
	} {
		if limit.value <= 0 {
			return fmt.Errorf("MCP %s must be positive", limit.name)
		}
	}
	return validateMCPServerIDs(cfg.Servers)
}

func validMCPID(id string) bool {
	if len(id) == 0 || len(id) > 63 || id[0] < 'a' || id[0] > 'z' {
		return false
	}
	for _, r := range id[1:] {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func mcpProjectPath(root string) string {
	return filepath.Clean(workspace.NamespacePath(root, "mivia.toml"))
}
