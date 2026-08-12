package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// TestRunConfiguredChatOnceOllamaLoopbackSkipsKeyGate pins that a local
// ollama loopback provider needs no API key: the entrypoint must get past the
// missing-key gate and reach the provider (failing with a dial error when no
// daemon is listening, or succeeding against a real one) instead of returning
// "missing API key". This is the chat-side pair of the config validateBaseURL
// relaxation for ollama loopback URLs.
func TestRunConfiguredChatOnceOllamaLoopbackSkipsKeyGate(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, ".mivia"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(ws, ".mivia", "mivia.toml")
	fixture := `[provider]
name = "ollama"

[providers.ollama]
base_url = "http://127.0.0.1:11434/v1"
models = [{ name = "llama3.1:8b", context_window_tokens = 128000 }]
`
	if err := os.WriteFile(cfgPath, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	// Defensive: whatever the outer environment carries, this test asserts the
	// loopback path needs no key.
	t.Setenv("OLLAMA_API_KEY", "")
	t.Chdir(ws)

	res, err := config.Load(config.LoadOptions{
		ConfigPath:    cfgPath,
		WorkspaceRoot: ws,
	})
	if err != nil {
		t.Fatalf("config.Load(ollama loopback): %v", err)
	}
	if res.ProviderName != "ollama" {
		t.Fatalf("provider = %q, want ollama", res.ProviderName)
	}

	err = runConfiguredChatOnceImpl(chatInvocation{
		prompt:        "hi",
		workspacePath: ws,
		noTools:       true,
	}, res)
	if err != nil && strings.Contains(err.Error(), "missing API key") {
		t.Fatalf("runConfiguredChatOnceImpl: %v (key gate must not fire for ollama loopback)", err)
	}
	// With no local daemon the call ends in a dial error - acceptable. If a
	// real daemon is running it may even succeed.
}
