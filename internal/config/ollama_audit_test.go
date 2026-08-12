package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeOllamaAuditConfig writes a single-provider config file. baseURL ""
// omits base_url; credVal "unset" unsets the provider API key env var; any
// other credVal is set via t.Setenv. envFilePath != "" adds a top-level
// env_file entry pointing at a key file.
func writeOllamaAuditConfig(t *testing.T, providerName, baseURL, modelName, credVal, envFilePath string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "mivia.toml")
	var b strings.Builder
	if envFilePath != "" {
		b.WriteString("env_file = \"" + envFilePath + "\"\n")
	}
	b.WriteString("[provider]\nname = \"" + providerName + "\"\n\n")
	b.WriteString("[providers." + providerName + "]\n")
	if baseURL != "" {
		b.WriteString("base_url = \"" + baseURL + "\"\n")
	}
	b.WriteString("models = [{ name = \"" + modelName + "\", context_window_tokens = 128000 }]\n")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	env := strings.ToUpper(providerName) + "_API_KEY"
	switch credVal {
	case "unset":
		os.Unsetenv(env)
	default:
		t.Setenv(env, credVal)
	}
	t.Setenv("HOME", t.TempDir())
	return path
}

// TestAuditOllamaEnvTrimming pins OLLAMA_API_KEY handling end to end: a
// whitespace-only value is treated as unset, a padded value is trimmed to
// non-empty, and the loopback keyless path never consults the env var for
// selectability.
func TestAuditOllamaEnvTrimming(t *testing.T) {
	path := writeOllamaAuditConfig(t, "ollama", "http://127.0.0.1:11434/v1", "gpt-oss:120b", "unset", "")
	tests := []struct {
		name    string
		set     bool
		value   string
		wantSet bool
	}{
		{"unset", false, "", false},
		{"empty", true, "", false},
		{"spacey", true, "  x  ", true},
		{"blank", true, "   ", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.set {
				t.Setenv("OLLAMA_API_KEY", tt.value)
			} else {
				os.Unsetenv("OLLAMA_API_KEY")
			}
			res, err := Load(LoadOptions{ConfigPath: path})
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if res.APIKeySet != tt.wantSet {
				t.Fatalf("APIKeySet = %v, want %v", res.APIKeySet, tt.wantSet)
			}
			group := findProviderGroup(res.ModelCatalog(), "ollama")
			if group == nil {
				t.Fatal("no catalog group for ollama")
			}
			if !group.Selectable {
				t.Fatalf("loopback ollama not selectable (DisabledReason=%q)", group.DisabledReason)
			}
		})
	}
}

// TestAuditOllamaKeyFileInterplay pins the env_file path: a key read from an
// explicit env_file is honored, a blank value in the file is treated as
// missing, and the loopback keyless path works with the env var unset.
func TestAuditOllamaKeyFileInterplay(t *testing.T) {
	os.Unsetenv("OLLAMA_API_KEY")
	t.Setenv("HOME", t.TempDir())
	keyFile := filepath.Join(t.TempDir(), "creds")
	writeKey := func(line string) {
		t.Helper()
		if err := os.WriteFile(keyFile, []byte("OLLAMA_API_KEY = "+line+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	writeKey("\"x\"")
	path := writeOllamaAuditConfig(t, "ollama", "https://ollama.com/v1", "gpt-oss:120b", "unset", keyFile)
	res, err := Load(LoadOptions{ConfigPath: path})
	if err != nil {
		t.Fatalf("Load with file key: %v", err)
	}
	if !res.APIKeySet {
		t.Fatal("APIKeySet = false with key present in env_file")
	}
	group := findProviderGroup(res.ModelCatalog(), "ollama")
	if group == nil || !group.Selectable {
		t.Fatalf("cloud ollama with file key not selectable (group=%+v)", group)
	}

	writeKey("")
	path = writeOllamaAuditConfig(t, "ollama", "https://ollama.com/v1", "gpt-oss:120b", "unset", keyFile)
	res, err = Load(LoadOptions{ConfigPath: path})
	if err != nil {
		t.Fatalf("Load with blank file key: %v", err)
	}
	if res.APIKeySet {
		t.Fatal("APIKeySet = true with blank value in env_file")
	}
	group = findProviderGroup(res.ModelCatalog(), "ollama")
	if group == nil || group.Selectable {
		t.Fatalf("cloud ollama with blank file key selectable (group=%+v)", group)
	}
	if group.DisabledReason != "credential unavailable" {
		t.Fatalf("DisabledReason = %q, want %q", group.DisabledReason, "credential unavailable")
	}
}

// ollamaSelectableMatrix drives TestAuditOllamaLoadAndSelectableMatrix over
// every base_url/key combination the config layer must distinguish: the
// loopback keyless relaxation must apply ONLY to provider "ollama" and only
// on loopback URLs; cloud URLs and every other provider keep requiring the
// key.
var ollamaSelectableMatrix = []struct {
	name         string
	provider     string
	baseURL      string
	credVal      string
	wantLoadErr  string
	wantBaseURL  string
	wantSelect   bool
	wantDisabled string
	wantKeySet   bool
}{
	{
		name:     "ollama no base_url no key defaults to cloud and requires key",
		provider: "ollama", baseURL: "", credVal: "",
		wantBaseURL: "https://ollama.com/v1", wantSelect: false,
		wantDisabled: "credential unavailable", wantKeySet: false,
	},
	{
		name:     "ollama no base_url with key is selectable",
		provider: "ollama", baseURL: "", credVal: "x",
		wantBaseURL: "https://ollama.com/v1", wantSelect: true, wantKeySet: true,
	},
	{
		name:     "ollama loopback no key is selectable",
		provider: "ollama", baseURL: "http://127.0.0.1:11434/v1", credVal: "",
		wantBaseURL: "http://127.0.0.1:11434/v1", wantSelect: true,
		wantDisabled: "", wantKeySet: false,
	},
	{
		name:     "ollama loopback empty env var still selectable",
		provider: "ollama", baseURL: "http://127.0.0.1:11434/v1", credVal: "",
		wantSelect: true, wantKeySet: false,
	},
	{
		name:     "ollama loopback whitespace key still selectable keyless",
		provider: "ollama", baseURL: "http://127.0.0.1:11434/v1", credVal: "   ",
		wantSelect: true, wantKeySet: false,
	},
	{
		name:     "ollama loopback with key selectable",
		provider: "ollama", baseURL: "http://127.0.0.1:11434/v1", credVal: "x",
		wantSelect: true, wantKeySet: true,
	},
	{
		name:     "ollama cloud no key not selectable",
		provider: "ollama", baseURL: "https://ollama.com/v1", credVal: "",
		wantSelect: false, wantDisabled: "credential unavailable", wantKeySet: false,
	},
	{
		name:     "ollama cloud with key selectable",
		provider: "ollama", baseURL: "https://ollama.com/v1", credVal: "x",
		wantSelect: true, wantKeySet: true,
	},
	{
		name:     "ollama https loopback no key selectable",
		provider: "ollama", baseURL: "https://127.0.0.1:11434/v1", credVal: "",
		wantSelect: true, wantKeySet: false,
	},
	{
		name:     "ollama localhost no key selectable",
		provider: "ollama", baseURL: "http://localhost:11434/v1", credVal: "",
		wantSelect: true, wantKeySet: false,
	},
	{
		name:     "ollama invalid url refused at load",
		provider: "ollama", baseURL: "not-a-url", credVal: "",
		wantLoadErr: "base_url",
	},
	{
		name:     "deepseek loopback http not relaxed (load error)",
		provider: "deepseek", baseURL: "http://127.0.0.1:11434/v1", credVal: "",
		wantLoadErr: "https",
	},
	{
		name:     "deepseek loopback https no key not selectable",
		provider: "deepseek", baseURL: "https://127.0.0.1:11434/v1", credVal: "",
		wantSelect: false, wantDisabled: "credential unavailable", wantKeySet: false,
	},
	{
		name:     "deepseek loopback https with key selectable",
		provider: "deepseek", baseURL: "https://127.0.0.1:11434/v1", credVal: "x",
		wantSelect: true, wantKeySet: true,
	},
	{
		name:     "openrouter on ollama cloud url still requires key",
		provider: "openrouter", baseURL: "https://ollama.com/v1", credVal: "",
		wantSelect: false, wantDisabled: "credential unavailable", wantKeySet: false,
	},
	{
		name:     "openrouter on ollama cloud url with key selectable",
		provider: "openrouter", baseURL: "https://ollama.com/v1", credVal: "x",
		wantSelect: true, wantKeySet: true,
	},
	{
		name:     "ollama whitespace key cloud not selectable",
		provider: "ollama", baseURL: "https://ollama.com/v1", credVal: "   ",
		wantSelect: false, wantDisabled: "credential unavailable", wantKeySet: false,
	},
}

func TestAuditOllamaLoadAndSelectableMatrix(t *testing.T) {
	const model = "gpt-oss:120b"
	for _, tt := range ollamaSelectableMatrix {
		t.Run(tt.name, func(t *testing.T) {
			path := writeOllamaAuditConfig(t, tt.provider, tt.baseURL, model, tt.credVal, "")
			res, err := Load(LoadOptions{ConfigPath: path})
			if tt.wantLoadErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantLoadErr) {
					t.Fatalf("Load error = %v, want containing %q", err, tt.wantLoadErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if tt.wantBaseURL != "" && res.BaseURL != tt.wantBaseURL {
				t.Fatalf("BaseURL = %q, want %q", res.BaseURL, tt.wantBaseURL)
			}
			if res.APIKeySet != tt.wantKeySet {
				t.Fatalf("APIKeySet = %v, want %v", res.APIKeySet, tt.wantKeySet)
			}
			group := findProviderGroup(res.ModelCatalog(), tt.provider)
			if group == nil {
				t.Fatalf("no catalog group for %q", tt.provider)
			}
			if group.Selectable != tt.wantSelect {
				t.Fatalf("group %q Selectable = %v, want %v (DisabledReason=%q)", tt.provider, group.Selectable, tt.wantSelect, group.DisabledReason)
			}
			if group.DisabledReason != tt.wantDisabled {
				t.Fatalf("group %q DisabledReason = %q, want %q", tt.provider, group.DisabledReason, tt.wantDisabled)
			}
			rt, ok := res.ProviderRuntimes[tt.provider]
			if !ok {
				t.Fatalf("no runtime for %q", tt.provider)
			}
			if rt.APIKeySet != tt.wantKeySet {
				t.Fatalf("runtime APIKeySet = %v, want %v", rt.APIKeySet, tt.wantKeySet)
			}
			if tt.wantBaseURL != "" && rt.BaseURL != tt.wantBaseURL {
				t.Fatalf("runtime BaseURL = %q, want %q", rt.BaseURL, tt.wantBaseURL)
			}
		})
	}
}

// TestAuditShippedExampleLoads pins that the shipped .mivia/mivia.toml.example
// loads with the real loader in every documented shape: as shipped (deepseek
// active, ollama cloud profile), with ollama active and no key, with ollama
// active and a key, and with the local-daemon profile (loopback, no key).
func TestAuditShippedExampleLoads(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", ".mivia", "mivia.toml.example"))
	if err != nil {
		t.Fatal(err)
	}
	os.Unsetenv("OLLAMA_API_KEY")
	t.Setenv("HOME", t.TempDir())
	writeCfg := func(data []byte) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "mivia.toml")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	loadExample := func(data []byte) (*Resolved, *ProviderModelGroup) {
		t.Helper()
		res, err := Load(LoadOptions{ConfigPath: writeCfg(data)})
		if err != nil {
			t.Fatalf("shipped example failed to load: %v", err)
		}
		group := findProviderGroup(res.ModelCatalog(), "ollama")
		if group == nil {
			t.Fatal("no ollama group in shipped example")
		}
		return res, group
	}

	// (a) As shipped: cloud profile under [providers.ollama]; no key.
	_, group := loadExample(raw)
	if group.Selectable {
		t.Fatal("shipped example ollama (cloud, no key) must be unselectable")
	}
	if group.DisabledReason != "credential unavailable" {
		t.Fatalf("DisabledReason = %q, want %q", group.DisabledReason, "credential unavailable")
	}

	// (b) Active provider switched to ollama, still no key: loads, key missing.
	cloud := bytes.ReplaceAll(raw, []byte("name = \"deepseek\""), []byte("name = \"ollama\""))
	_, group = loadExample(cloud)
	if group.Selectable {
		t.Fatal("ollama cloud without key must be unselectable")
	}

	// (c) Active ollama + key: selectable.
	t.Setenv("OLLAMA_API_KEY", "x")
	res, group := loadExample(cloud)
	if !res.APIKeySet || !group.Selectable {
		t.Fatalf("ollama cloud with key must be selectable (APIKeySet=%v group=%+v)", res.APIKeySet, group)
	}

	// (d) Local-daemon profile: loopback base_url, no api_key_env line.
	local := bytes.ReplaceAll(cloud, []byte("api_key_env = \"OLLAMA_API_KEY\"\n"), []byte(""))
	local = bytes.ReplaceAll(local, []byte("base_url = \"https://ollama.com/v1\"\n"), []byte("base_url = \"http://127.0.0.1:11434/v1\"\n"))
	os.Unsetenv("OLLAMA_API_KEY")
	_, group = loadExample(local)
	if !group.Selectable {
		t.Fatalf("ollama local-daemon profile must be selectable without a key (group=%+v)", group)
	}
}
