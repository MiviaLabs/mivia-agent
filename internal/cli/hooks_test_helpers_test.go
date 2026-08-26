package cli

import (
	"os"
	"path/filepath"
	"testing"
)

const twoHookConfig = `[[hooks]]
event = "PreToolUse"
matcher = "run_command"

  [[hooks.handlers]]
  type = "command"
  argv = ["./gate.sh"]

[[hooks]]
event = "PostToolUse"

  [[hooks.handlers]]
  type = "command"
  argv = ["./fmt.sh"]
`

// hookHome points HOME at a fresh directory holding a user config. The
// workspace it returns has an empty .mivia/ - use writeWorkspaceHooks to give
// that project hooks of its own.
func hookHome(t *testing.T, body string) (home, ws string) {
	t.Helper()
	base := t.TempDir()
	home = filepath.Join(base, "home")
	ws = filepath.Join(base, "ws")
	for _, dir := range []string{filepath.Join(home, ".mivia"), filepath.Join(ws, ".mivia")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, ".mivia", "mivia.toml"), []byte(body), 0o600); err != nil {
		t.Fatalf("write user config: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("MIVIA_CONFIG", "")
	return home, ws
}

func writeWorkspaceHooks(t *testing.T, ws, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(ws, ".mivia", "mivia.toml"), []byte(body), 0o600); err != nil {
		t.Fatalf("write workspace config: %v", err)
	}
}
