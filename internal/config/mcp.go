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
		return user.resolve(MCPConfig{}), nil, nil
	}
	project, err := loadMCPConfigPath(projectPath)
	if err != nil {
		return MCPConfig{}, nil, err
	}
	return mergeMCPConfig(user, project).resolve(MCPConfig{}), nil, nil
}

type mcpConfigInput struct {
	Enabled                 *bool
	StartupTimeoutSeconds   *int
	MaxServers              *int
	MaxToolsPerServer       *int
	MaxToolSchemaBytes      *int
	MaxToolDescriptionBytes *int
	MaxToolResultBytes      *int
	Servers                 []MCPServerConfig
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
	return *document.MCP, nil
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
	out := MCPConfig{Servers: append([]MCPServerConfig(nil), in.Servers...)}
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
	return nil
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
