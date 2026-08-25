package config

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
	"github.com/pelletier/go-toml/v2"
)

var mcpEnvironmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var mcpHeaderName = regexp.MustCompile(`^[!#$%&'*+.^_` + "`" + `|~0-9A-Za-z-]+$`)

var mcpTransportHeaders = map[string]struct{}{
	"accept":         {},
	"content-type":   {},
	"last-event-id":  {},
	"mcp-session-id": {},
}

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

// LoadScopeMCPServers loads user and project MCP server configurations separately without merging.
func LoadScopeMCPServers(userPath, projectPath string) (userServers, projectServers []MCPServerConfig, err error) {
	if userPath != "" {
		u, err := loadMCPConfigPath(userPath)
		if err == nil && u.Servers != nil {
			userServers = u.Servers
		}
	}
	if projectPath != "" && !sameFilePath(userPath, projectPath) {
		p, err := loadMCPConfigPath(projectPath)
		if err == nil && p.Servers != nil {
			projectServers = p.Servers
		}
	}
	return userServers, projectServers, nil
}

func resolveMCPConfig(input mcpConfigInput) (MCPConfig, []string, error) {
	resolved := input.resolve(MCPConfig{})
	if err := validateResolvedMCPConfig(resolved); err != nil {
		return MCPConfig{}, nil, err
	}
	warnings := make([]string, 0)
	if resolved.Enabled {
		for _, server := range resolved.Servers {
			if server.Transport == "streamable_http" && strings.HasPrefix(server.URL, "http://") {
				warnings = append(warnings, fmt.Sprintf("MCP server %q uses plaintext HTTP", server.ID))
			}
		}
	}
	return resolved, warnings, nil
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
	out.StartupTimeoutSeconds = tighterMCPBound(out.StartupTimeoutSeconds, project.StartupTimeoutSeconds)
	out.MaxServers = tighterMCPBound(out.MaxServers, project.MaxServers)
	out.MaxToolsPerServer = tighterMCPBound(out.MaxToolsPerServer, project.MaxToolsPerServer)
	out.MaxToolSchemaBytes = tighterMCPBound(out.MaxToolSchemaBytes, project.MaxToolSchemaBytes)
	out.MaxToolDescriptionBytes = tighterMCPBound(out.MaxToolDescriptionBytes, project.MaxToolDescriptionBytes)
	out.MaxToolResultBytes = tighterMCPBound(out.MaxToolResultBytes, project.MaxToolResultBytes)
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

func tighterMCPBound(user, project *int) *int {
	if user == nil {
		return project
	}
	if project == nil || *user <= *project {
		return user
	}
	return project
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
	if server.TimeoutSeconds < 0 {
		return fmt.Errorf("timeout_seconds must not be negative")
	}
	switch server.Transport {
	case "stdio":
		// A command that is absolute in either path convention is accepted.
		// The shipped project config uses a POSIX-absolute command that has
		// no Windows drive letter; rejecting it on Windows would make the
		// whole repository config unloadable there. The command itself is
		// still never resolved from PATH, and a server whose command cannot
		// run on this platform fails cleanly at connect time.
		if !filepath.IsAbs(server.Command) && !path.IsAbs(server.Command) {
			return fmt.Errorf("stdio command must be absolute")
		}
		if server.URL != "" {
			return fmt.Errorf("stdio server must not set url")
		}
		if len(server.Headers) != 0 {
			return fmt.Errorf("stdio server must not set headers")
		}
	case "streamable_http":
		u, err := url.Parse(server.URL)
		if err != nil || !u.IsAbs() || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Fragment != "" {
			return fmt.Errorf("streamable_http url is invalid")
		}
		if server.Command != "" || len(server.Args) != 0 || len(server.Env) != 0 {
			return fmt.Errorf("streamable_http server must not set stdio fields")
		}
	default:
		return fmt.Errorf("transport %q is unsupported", server.Transport)
	}
	for _, name := range server.Env {
		if !mcpEnvironmentName.MatchString(name) {
			return fmt.Errorf("environment name %q is invalid", name)
		}
	}
	for _, arg := range server.Args {
		if strings.ContainsAny(arg, "\x00\r\n") {
			return fmt.Errorf("args must not contain control characters")
		}
	}
	seenHeaders := make(map[string]struct{}, len(server.Headers))
	for _, header := range server.Headers {
		name := strings.ToLower(header.Name)
		if !mcpHeaderName.MatchString(header.Name) || !mcpEnvironmentName.MatchString(header.ValueEnv) {
			return fmt.Errorf("header is invalid")
		}
		if _, owned := mcpTransportHeaders[name]; owned {
			return fmt.Errorf("header %q is transport-owned", header.Name)
		}
		if _, duplicate := seenHeaders[name]; duplicate {
			return fmt.Errorf("header %q is duplicated", header.Name)
		}
		seenHeaders[name] = struct{}{}
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
