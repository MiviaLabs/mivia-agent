// Package mcp implements MCP client configuration and tool adapters.
package mcp

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

var environmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var headerName = regexp.MustCompile(`^[!#$%&'*+.^_` + "`" + `|~0-9A-Za-z-]+$`)

// ValidateServerConfig validates one secret-free MCP server definition.
func ValidateServerConfig(server config.MCPServerConfig) error {
	if server.ID == "" {
		return fmt.Errorf("server ID is empty")
	}
	if !validServerID(server.ID) {
		return fmt.Errorf("server ID %q is invalid", server.ID)
	}
	if server.TimeoutSeconds < 0 {
		return fmt.Errorf("server timeout is negative")
	}
	switch server.Transport {
	case "stdio":
		if !filepath.IsAbs(server.Command) {
			return fmt.Errorf("stdio command must be absolute")
		}
		info, err := os.Stat(server.Command)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
			return fmt.Errorf("stdio command must be an executable regular file")
		}
		if server.URL != "" {
			return fmt.Errorf("stdio server must not set URL")
		}
		if len(server.Headers) != 0 {
			return fmt.Errorf("stdio server must not set HTTP headers")
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
	for _, arg := range server.Args {
		if strings.ContainsAny(arg, "\x00\r\n") {
			return fmt.Errorf("stdio argument contains a control character")
		}
	}
	seenHeaders := make(map[string]struct{}, len(server.Headers))
	for _, header := range server.Headers {
		name := strings.ToLower(header.Name)
		if !environmentName.MatchString(header.ValueEnv) || !headerName.MatchString(header.Name) {
			return fmt.Errorf("MCP header is invalid")
		}
		if _, ok := seenHeaders[name]; ok {
			return fmt.Errorf("MCP header %q is duplicated", header.Name)
		}
		seenHeaders[name] = struct{}{}
	}
	return nil
}

func validServerID(id string) bool {
	if len(id) == 0 || len(id) > 63 || id[0] < 'a' || id[0] > 'z' {
		return false
	}
	for _, value := range id[1:] {
		if value >= 'a' && value <= 'z' || value >= '0' && value <= '9' || value == '_' || value == '-' {
			continue
		}
		return false
	}
	return true
}

// EncodeToolName makes a host-safe and reversible MCP tool name.
func EncodeToolName(serverID, remoteName string) (string, error) {
	if !validServerID(serverID) || remoteName == "" {
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
