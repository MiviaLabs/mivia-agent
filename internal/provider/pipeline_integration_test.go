package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

func TestConfigLoadProviderNewChatTurn(t *testing.T) {
	t.Setenv("MIVIA_ALLOW_INSECURE_HTTP", "1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer integration-key" {
			t.Fatalf("authorization=%q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": "integrated"}, "finish_reason": "stop"}}})
	}))
	defer srv.Close()
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	configPath := filepath.Join(dir, "mivia.toml")
	if err := os.WriteFile(envPath, []byte("DEEPSEEK_API_KEY=integration-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	data := "[provider]\nname = \"deepseek\"\nenv_file = \"" + filepath.ToSlash(envPath) + "\"\n\n[providers.deepseek]\nbase_url = \"" + srv.URL + "\"\n"
	if err := os.WriteFile(configPath, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, err := config.Load(config.LoadOptions{ConfigPath: configPath})
	if err != nil {
		t.Fatal(err)
	}
	completer, err := New(resolved)
	if err != nil {
		t.Fatal(err)
	}
	response, err := completer.ChatTurn(context.Background(), Request{Model: resolved.Model, Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil || response.Content != "integrated" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}
