// Package mcp implements MCP client configuration and tool adapters.
package mcp

import (
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

var environmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidateServerConfig validates one secret-free MCP server definition.
func ValidateServerConfig(server config.MCPServerConfig) error {
	if server.ID == "" {
		return fmt.Errorf("server ID is empty")
	}
	switch server.Transport {
	case "stdio":
		if !filepath.IsAbs(server.Command) {
			return fmt.Errorf("stdio command must be absolute")
		}
		if server.URL != "" {
			return fmt.Errorf("stdio server must not set URL")
		}
	case "streamable_http":
		u, err := url.Parse(server.URL)
		if err != nil || !u.IsAbs() || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Fragment != "" {
			return fmt.Errorf("streamable_http URL is invalid")
		}
		if server.Command != "" || len(server.Args) != 0 || len(server.Env) != 0 {
			return fmt.Errorf("streamable_http server must not set stdio fields")
		}
	default:
		return fmt.Errorf("unsupported MCP transport %q", server.Transport)
	}
	for _, name := range server.Env {
		if !environmentName.MatchString(name) {
			return fmt.Errorf("environment name %q is invalid", name)
		}
	}
	for _, header := range server.Headers {
		if !environmentName.MatchString(header.ValueEnv) || strings.TrimSpace(header.Name) == "" {
			return fmt.Errorf("MCP header is invalid")
		}
	}
	return nil
}

// EncodeToolName makes a host-safe and reversible MCP tool name.
func EncodeToolName(serverID, remoteName string) (string, error) {
	if serverID == "" || remoteName == "" {
		return "", fmt.Errorf("MCP tool name is empty")
	}
	const hex = "0123456789abcdef"
	if len(remoteName) > 48 {
		return "", fmt.Errorf("MCP tool name is too long")
	}
	var b strings.Builder
	b.Grow(len("mcp__") + len(serverID) + 3 + 1 + len(remoteName)*2)
	b.WriteString("mcp__")
	b.WriteString(serverID)
	b.WriteString("__x")
	for _, c := range []byte(remoteName) {
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&15])
	}
	return b.String(), nil
}
