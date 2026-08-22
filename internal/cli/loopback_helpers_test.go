package cli

// closedLoopbackPort returns a currently-free loopback port. Duplicated from
// internal/cliorchestrate's ollama audit test (Go forbids cross-package
// _test.go sharing).

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"net"
	"testing"
)

func closedLoopbackPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// writeOllamaChatConfig writes a HOME-isolated single-provider ollama config
// with the given base_url and returns the loaded Resolved plus workspace.
// Duplicated from internal/cliorchestrate's ollama audit test.
func writeOllamaChatConfig(t *testing.T, baseURL string) (*config.Resolved, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OLLAMA_API_KEY", "")
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, ".mivia"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(ws, ".mivia", "mivia.toml")
	fixture := fmt.Sprintf(`[provider]
name = "ollama"

[providers.ollama]
base_url = %q
models = [{ name = "llama3.1:8b", context_window_tokens = 128000 }]
`, baseURL)
	if err := os.WriteFile(cfgPath, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := config.Load(config.LoadOptions{ConfigPath: cfgPath, WorkspaceRoot: ws})
	if err != nil {
		t.Fatalf("config.Load(%s): %v", baseURL, err)
	}
	return res, ws
}
