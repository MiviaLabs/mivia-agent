package cli

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// TestRunConfiguredChatOnceOllamaLoopbackSkipsKeyGate pins that a local
// ollama loopback provider needs no API key: the entrypoint must get past the
// missing-key gate and reach the provider (failing with a provider error from
// a hermetic 404 server) instead of returning "missing API key". This is the
// chat-side pair of the config validateBaseURL relaxation for ollama loopback
// URLs.
func TestRunConfiguredChatOnceOllamaLoopbackSkipsKeyGate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)

	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, ".mivia"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(ws, ".mivia", "mivia.toml")
	fixture := fmt.Sprintf(`[provider]
name = "ollama"

[providers.ollama]
base_url = "%s/v1"
models = [{ name = "llama3.1:8b", context_window_tokens = 128000 }]
`, server.URL)
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
	// The call must reach the provider: the hermetic 404 server answers, so
	// the error is a provider error and never the key gate.
}

// TestRunConfiguredChatOnceWhitespaceKeyIsMissing pins that a whitespace-only
// OLLAMA_API_KEY is treated as missing for a cloud ollama endpoint: the
// entrypoint must fail at its own key gate with the doctor-referencing message,
// not let the whitespace key through to surface a worse error one layer down in
// the provider factory.
func TestRunConfiguredChatOnceWhitespaceKeyIsMissing(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, ".mivia"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(ws, ".mivia", "mivia.toml")
	fixture := `[provider]
name = "ollama"

[providers.ollama]
base_url = "https://ollama.example.com/v1"
models = [{ name = "llama3.1:8b", context_window_tokens = 128000 }]
`
	if err := os.WriteFile(cfgPath, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OLLAMA_API_KEY", "   ")
	t.Chdir(ws)

	res, err := config.Load(config.LoadOptions{
		ConfigPath:    cfgPath,
		WorkspaceRoot: ws,
	})
	if err != nil {
		t.Fatalf("config.Load(ollama cloud): %v", err)
	}
	if res.ProviderName != "ollama" {
		t.Fatalf("provider = %q, want ollama", res.ProviderName)
	}

	err = runConfiguredChatOnceImpl(chatInvocation{
		prompt:        "hi",
		workspacePath: ws,
		noTools:       true,
	}, res)
	if err == nil {
		t.Fatal("runConfiguredChatOnceImpl: expected missing-key error, got nil")
	}
	if !strings.Contains(err.Error(), "missing API key: set") {
		t.Fatalf("runConfiguredChatOnceImpl error = %q, want it to contain %q", err, "missing API key: set")
	}
}

// TestRunConfiguredChatOnceOllamaLoopbackWhitespaceKeySkipsKeyGate pins that the
// ollama loopback relaxation survives a whitespace-only OLLAMA_API_KEY: the key
// gate must not fire, and the call must reach the provider (failing with a
// provider error from a hermetic 404 server) instead of returning "missing API
// key".
func TestRunConfiguredChatOnceOllamaLoopbackWhitespaceKeySkipsKeyGate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)

	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, ".mivia"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(ws, ".mivia", "mivia.toml")
	fixture := fmt.Sprintf(`[provider]
name = "ollama"

[providers.ollama]
base_url = "%s/v1"
models = [{ name = "llama3.1:8b", context_window_tokens = 128000 }]
`, server.URL)
	if err := os.WriteFile(cfgPath, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OLLAMA_API_KEY", "   ")
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
		t.Fatalf("runConfiguredChatOnceImpl: %v (key gate must not fire for ollama loopback with whitespace key)", err)
	}
	// With no local daemon the call ends in a dial error - acceptable. If a
	// real daemon is running it may even succeed.
}
