package cliorchestrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeDoctorConfig(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "mivia.toml")
	body := `[provider]
name = "deepseek"

[providers.deepseek]
models = [{ name = "deepseek-v4-pro", context_window_tokens = 128000 }]
default_model = "deepseek-v4-pro"
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDoctorReportsAgentsBeforeMissingCredentialError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DEEPSEEK_API_KEY", "")
	root := t.TempDir()
	configPath := writeDoctorConfig(t, root)
	workspace := t.TempDir()
	writeCatalogAgent(t, filepath.Join(workspace, ".agents", "agents"), "local", "---\nname: local\ndescription: safe\n---\n")
	var out, errOut strings.Builder
	err := RunDoctorWithIO([]string{"--config", configPath, "--workspace", workspace}, &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "missing DEEPSEEK_API_KEY") {
		t.Fatalf("doctor error = %v", err)
	}
	if !strings.Contains(out.String(), "name: local") || !strings.Contains(out.String(), "workspace agent files: always loaded") {
		t.Fatalf("agent diagnostics missing before readiness failure: %s", out.String())
	}
}

func TestDoctorSeparatesEmptyAgentsFromWorkspacePromptGate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DEEPSEEK_API_KEY", "example-token")
	root := t.TempDir()
	configPath := writeDoctorConfig(t, root)
	workspace := t.TempDir()
	var out, errOut strings.Builder
	if err := RunDoctorWithIO([]string{"--config", configPath, "--workspace", workspace}, &out, &errOut); err != nil {
		t.Fatalf("doctor error = %v", err)
	}
	text := out.String()
	// File-collection emptiness ("collection: not present") must stay
	// distinguishable from the workspace prompt gate, while the compiled
	// built-in still shows as a selectable roster row.
	if !strings.Contains(text, "collection: not present") || !strings.Contains(text, "workspace prompts/project skills: enabled") {
		t.Fatalf("empty collection and gate are conflated: %s", text)
	}
	if !strings.Contains(text, "name: general-purpose") || !strings.Contains(text, "state: selectable") {
		t.Fatalf("built-in roster row missing: %s", text)
	}
}
