package clichat

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

// hermeticOllamaLoopbackWorkspace builds a workspace whose config points at a
// hermetic 404 ollama loopback server, so the entrypoint passes its key gate
// without a daemon. Returns the workspace path.
func hermeticOllamaLoopbackWorkspace(t *testing.T) string {
	t.Helper()
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
	return ws
}

func loadChatTestConfig(t *testing.T, ws string) *config.Resolved {
	t.Helper()
	res, err := config.Load(config.LoadOptions{ConfigPath: filepath.Join(ws, ".mivia", "mivia.toml"), WorkspaceRoot: ws})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return res
}

// TestRunConfiguredChatOnceResumeFailureReleasesLedgerStore pins the failure
// path: a tools-on run whose --session cannot load must release the adopted
// session ledger store before returning, not leak it for the process life.
func TestRunConfiguredChatOnceResumeFailureReleasesLedgerStore(t *testing.T) {
	ws := hermeticOllamaLoopbackWorkspace(t)
	res := loadChatTestConfig(t, ws)

	err := runConfiguredChatOnceImpl(chatInvocation{
		prompt:        "hi",
		workspacePath: ws,
		session:       "ghost-session",
	}, res)
	if err == nil {
		t.Fatal("runConfiguredChatOnceImpl: expected the missing-session error, got nil")
	}
	if !strings.Contains(err.Error(), `--session "ghost-session"`) {
		t.Fatalf("error = %q, want the resume failure naming the session", err)
	}
}

// TestRunConfiguredChatOnceContextSetupFailureReleasesLedgerStore pins the
// second failure path: an unopenable context store fails setupChatSessionContext
// after adoption, and the adopted store must be released on that return too.
func TestRunConfiguredChatOnceContextSetupFailureReleasesLedgerStore(t *testing.T) {
	ws := hermeticOllamaLoopbackWorkspace(t)
	// Point store_path at the context store file, then make that file a
	// directory: OpenSQLite cannot open a directory, so
	// setupChatSessionContext fails after the ledger store was adopted.
	cfgPath := filepath.Join(ws, ".mivia", "mivia.toml")
	existing, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	fixture := "[subagents]\nstore_path = '.mivia/context.db'\n"
	if err := os.WriteFile(cfgPath, append(existing, []byte(fixture)...), 0o600); err != nil {
		t.Fatal(err)
	}
	res := loadChatTestConfig(t, ws)
	if err := os.MkdirAll(filepath.Join(ws, ".mivia", "context.db"), 0o700); err != nil {
		t.Fatal(err)
	}

	err = runConfiguredChatOnceImpl(chatInvocation{
		prompt:        "hi",
		workspacePath: ws,
	}, res)
	if err == nil {
		t.Fatal("runConfiguredChatOnceImpl: expected the context-store failure, got nil")
	}
	if !strings.Contains(err.Error(), "open context store") {
		t.Fatalf("error = %q, want the context-store open failure", err)
	}
}

// TestRunConfiguredChatOnceMemoryStoreFailureReleasesLedgerStore pins the
// first failure path: an unopenable memory store fails
// ConfigureChatWorkspace after the ledger store was adopted, and the
// adopted store must be released on that return too.
func TestRunConfiguredChatOnceMemoryStoreFailureReleasesLedgerStore(t *testing.T) {
	ws := hermeticOllamaLoopbackWorkspace(t)
	res := loadChatTestConfig(t, ws)
	// The Markdown backend must fail while scanning an invalid source file.
	if err := os.MkdirAll(filepath.Join(ws, ".agents", "memories"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".agents", "memories", "broken.md"), []byte("not a memory document\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := runConfiguredChatOnceImpl(chatInvocation{
		prompt:        "hi",
		workspacePath: ws,
	}, res)
	if err == nil {
		t.Fatal("runConfiguredChatOnceImpl: expected the memory-store failure, got nil")
	}
	if !strings.Contains(err.Error(), "memory store") {
		t.Fatalf("error = %q, want the memory-store open failure", err)
	}
}
