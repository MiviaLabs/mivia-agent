package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/envfile"
)

// Moved from ollama_r4_hostile_audit_test.go during the clichat extraction:
// this focus exercises cli's setup path, which stayed in this package.
// Focus 4: keyed setup through the real runSetupWithIO path writes the env
// file with OLLAMA_API_KEY and renders the keyed summary lines (Round-3
// printSetupSummary refactor preserved the keyed-path format).
func TestR4SetupOllamaWithKeyWritesEnvFile(t *testing.T) {
	t.Setenv("OLLAMA_API_KEY", "")
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	cfgPath := filepath.Join(dir, "mivia.toml")
	out, err := runSetupCapture(t, []string{
		"--provider", "ollama",
		"--key", "sk-r4-key",
		"--env-file", envPath,
		"--config", cfgPath,
		"--yes",
	}, "")
	if err != nil {
		t.Fatalf("setup error = %v", err)
	}
	entries, err := envfile.Load(envPath)
	if err != nil {
		t.Fatalf("load written env file: %v", err)
	}
	if entries["OLLAMA_API_KEY"] != "sk-r4-key" {
		t.Fatalf("env key = %q, want sk-r4-key", entries["OLLAMA_API_KEY"])
	}
	for _, want := range []string{
		"mivia setup",
		"  provider:   ollama",
		"  key env:    OLLAMA_API_KEY",
		"  key file:   " + envPath + " (written)",
		"  next:       run `mivia doctor` to verify",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("keyed summary missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "sk-r4-key") {
		t.Fatalf("summary leaks the key value:\n%s", out)
	}
}
