package mcp

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

func TestValidateServerConfig(t *testing.T) {
	valid := config.MCPServerConfig{
		ID: "repository", Transport: "stdio", Command: "/bin/echo", Args: []string{"serve"}, Env: []string{"TOKEN"},
	}
	if err := ValidateServerConfig(valid); err != nil {
		t.Fatalf("ValidateServerConfig(valid) error = %v", err)
	}
	if err := ValidateServerConfig(config.MCPServerConfig{ID: "bad", Transport: "stdio", Command: "echo"}); err == nil {
		t.Fatal("ValidateServerConfig accepted relative stdio command")
	}
	if err := ValidateServerConfig(config.MCPServerConfig{ID: "bad", Transport: "streamable_http", URL: "ftp://example.invalid/mcp"}); err == nil {
		t.Fatal("ValidateServerConfig accepted unsupported HTTP URL")
	}
}

func TestValidateServerConfigRejectsUnsafeHeadersAndArguments(t *testing.T) {
	base := config.MCPServerConfig{ID: "repository", Transport: "stdio", Command: "/bin/echo"}
	base.Args = []string{"line\nbreak"}
	if err := ValidateServerConfig(base); err == nil {
		t.Fatal("ValidateServerConfig accepted a control character in an argument")
	}
	http := config.MCPServerConfig{
		ID: "repository", Transport: "streamable_http", URL: "https://example.test/mcp",
		Headers: []config.MCPHeaderConfig{{Name: "Authorization", ValueEnv: "TOKEN"}, {Name: "authorization", ValueEnv: "OTHER"}},
	}
	if err := ValidateServerConfig(http); err == nil {
		t.Fatal("ValidateServerConfig accepted duplicate HTTP headers")
	}
	http.Headers = []config.MCPHeaderConfig{{Name: "Mcp-Session-Id", ValueEnv: "SESSION"}}
	if err := ValidateServerConfig(http); err == nil {
		t.Fatal("ValidateServerConfig accepted a transport-owned HTTP header")
	}
}

func TestEncodeToolNameIsDistinctAndBounded(t *testing.T) {
	first, err := EncodeToolName("repository", "read.file")
	if err != nil {
		t.Fatal(err)
	}
	second, err := EncodeToolName("repository", "read_file")
	if err != nil {
		t.Fatal(err)
	}
	if first == second || len(first) > 128 {
		t.Fatalf("encoded names = %q, %q", first, second)
	}
}

func TestEncodeToolNameRejectsUnsafeServerID(t *testing.T) {
	if _, err := EncodeToolName("bad/id", "read"); err == nil {
		t.Fatal("EncodeToolName accepted an unsafe server ID")
	}
}
