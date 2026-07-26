package envfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	content := "# comment\nDEEPSEEK_API_KEY=sk-test\nexport OPENROUTER_API_KEY=\"or-key\"\nEMPTY=\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if m["DEEPSEEK_API_KEY"] != "sk-test" {
		t.Fatalf("deepseek key: %q", m["DEEPSEEK_API_KEY"])
	}
	if m["OPENROUTER_API_KEY"] != "or-key" {
		t.Fatalf("openrouter key: %q", m["OPENROUTER_API_KEY"])
	}
}

func TestLookupProcessWins(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "from-process")
	file := map[string]string{"DEEPSEEK_API_KEY": "from-file"}
	v, ok := Lookup("DEEPSEEK_API_KEY", file)
	if !ok || v != "from-process" {
		t.Fatalf("got %q ok=%v", v, ok)
	}
}
