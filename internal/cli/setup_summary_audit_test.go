package cli

// Hostile audit (0dccb870..HEAD) of the Round-3 printSetupSummary extraction:
// the KEYED path must render byte-identical output to the pre-extraction
// format (same lines, same order), the keyless path must carry the
// conditional 'next' guidance.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The keyed-path summary is the exact pre-Round-3 line sequence. The
// extraction must not have reordered or reworded any keyed line.
func TestAuditSetupSummaryKeyedPathByteIdentical(t *testing.T) {
	var buf bytes.Buffer
	err := printSetupSummary(&buf, "deepseek", "DEEPSEEK_API_KEY", "/tmp/foo/a"+"env", "/tmp/foo/mivia.toml", false, true)
	if err != nil {
		t.Fatal(err)
	}
	want := "mivia setup\n" +
		"  provider:   deepseek\n" +
		"  key env:    DEEPSEEK_API_KEY\n" +
		"  key file:   /tmp/foo/a" + "env (written)\n" +
		"  config:     /tmp/foo/mivia.toml (written)\n" +
		"  next:       run `mivia doctor` to verify\n"
	if buf.String() != want {
		t.Errorf("keyed summary changed.\n--- got ---\n%s\n--- want ---\n%s", buf.String(), want)
	}
}

// The keyless path must describe both modes and end with the conditional
// 'next' guidance (add a [providers.ollama] block), never the plain doctor
// line, and must not claim a key file was written.
func TestAuditSetupSummaryKeylessConditionalNext(t *testing.T) {
	var buf bytes.Buffer
	err := printSetupSummary(&buf, "ollama", "OLLAMA_API_KEY", "/tmp/foo/a"+"env", "/tmp/foo/mivia.toml", true, false)
	if err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Contains(out, "key file:") || strings.Contains(out, "(written)") {
		t.Fatalf("keyless summary claims a written key file:\n%s", out)
	}
	if !strings.Contains(out, "local daemon") || !strings.Contains(out, "Ollama Cloud") {
		t.Fatalf("keyless summary must describe both modes:\n%s", out)
	}
	if !strings.Contains(out, "next:       add a [providers.ollama] block to your config, then run mivia doctor") {
		t.Fatalf("keyless summary lacks the conditional next line:\n%s", out)
	}
	if strings.Contains(out, "next:       run `mivia doctor` to verify\n") {
		t.Fatalf("keyless summary must not use the plain keyed next line:\n%s", out)
	}
}

// Hermeticity: runSetupWithIO with explicit --env-file/--config must never
// read or write anything under $HOME, even when a hostile process env var
// and pre-existing $HOME files could otherwise be picked up.
func TestAuditSetupHermeticWithExplicitPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MIVIA_TRAP_VAR", "trap-value")
	envName := "." + "env"
	homeEnv := filepath.Join(home, envName)
	before := []byte("HOME_MARKER" + "=" + "keep-me\n")
	if err := os.WriteFile(homeEnv, before, 0o600); err != nil {
		t.Fatal(err)
	}
	homeCfg := filepath.Join(home, "mivia.toml")
	homeCfgBefore := []byte("[provider]\nname = \"ollama\"\n")
	if err := os.WriteFile(homeCfg, homeCfgBefore, 0o600); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	envPath := filepath.Join(dir, envName)
	cfgPath := filepath.Join(dir, "mivia.toml")
	if _, err := runSetupCapture(t, []string{
		"--provider", "ollama",
		"--env-file", envPath,
		"--config", cfgPath,
		"--yes",
	}, ""); err != nil {
		t.Fatalf("setup error = %v", err)
	}
	// Keyless: no env file may be created anywhere, including $HOME.
	after, err := os.ReadFile(homeEnv)
	if err != nil {
		t.Fatalf("home env file disturbed: %v", err)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("home env file mutated: %q -> %q", before, after)
	}
	afterCfg, err := os.ReadFile(homeCfg)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(afterCfg, homeCfgBefore) {
		t.Fatalf("home config mutated: %q", afterCfg)
	}
}
