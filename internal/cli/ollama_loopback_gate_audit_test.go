package cli

// Hostile audit (0dccb870..HEAD) of the ollama loopback chat gate: the
// keyless loopback path must get PAST the missing-key gate and surface a
// provider error (the httptest 404 server), never a missing-key error. This
// pins the Round-3 claim that the loopback chat tests still prove 'gate must
// not fire' against a hermetic httptest server.

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

func TestAuditLoopbackChatGateSurfacesProvider404(t *testing.T) {
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
	t.Setenv("OLLAMA_API_KEY", "")
	t.Chdir(ws)

	res, err := config.Load(config.LoadOptions{ConfigPath: cfgPath, WorkspaceRoot: ws})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	err = runConfiguredChatOnceImpl(chatInvocation{prompt: "hi", workspacePath: ws, noTools: true}, res)
	if err == nil {
		t.Fatal("expected an error from the hermetic 404 provider, got nil")
	}
	if strings.Contains(err.Error(), "missing API key") {
		t.Fatalf("key gate fired: %v", err)
	}
	if !strings.Contains(err.Error(), "404") {
		t.Fatalf("error = %q, want a provider 404 error (gate must not fire and the call must reach the provider)", err)
	}
	t.Logf("loopback chat gate surface error: %v", err)
}

// The cloud ollama path must STILL fail at the key gate with the doctor-
// referencing message, even with a whitespace-only key value.
func TestAuditCloudOllamaWhitespaceKeyFailsAtGate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
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

	res, err := config.Load(config.LoadOptions{ConfigPath: cfgPath, WorkspaceRoot: ws})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	err = runConfiguredChatOnceImpl(chatInvocation{prompt: "hi", workspacePath: ws, noTools: true}, res)
	if err == nil || !strings.Contains(err.Error(), "missing API key: set") {
		t.Fatalf("err = %v, want the key-gate message", err)
	}
}
