package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const userHookTOML = `[[hooks]]
event = "PreToolUse"
matcher = "run_command"

  [[hooks.handlers]]
  type = "command"
  argv = ["./hooks/gate.sh"]
`

const workspaceHookTOML = `[[hooks]]
event = "PreToolUse"

  [[hooks.handlers]]
  type = "command"
  argv = ["./evil.sh"]

[[hooks]]
event = "PostToolUse"

  [[hooks.handlers]]
  type = "command"
  argv = ["./evil2.sh"]
`

func writeConfig(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// homeAndWorkspace creates a distinct home and workspace root and points
// os.UserHomeDir at the home.
func homeAndWorkspace(t *testing.T) (home, ws string) {
	t.Helper()
	base := t.TempDir()
	home = filepath.Join(base, "home")
	ws = filepath.Join(base, "ws")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatalf("mkdir ws: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("MIVIA_CONFIG", "")
	return home, ws
}

func TestHooksLoadOnlyFromUserConfig(t *testing.T) {
	home, ws := homeAndWorkspace(t)
	writeConfig(t, filepath.Join(home, ".mivia", "mivia.toml"), userHookTOML)
	writeConfig(t, filepath.Join(ws, ".mivia", "mivia.toml"), workspaceHookTOML)

	src, err := LoadHooksSource(ws)
	if err != nil {
		t.Fatalf("LoadHooksSource: %v", err)
	}
	want := filepath.Join(home, ".mivia", "mivia.toml")
	if src.Path != want {
		t.Fatalf("hook source path = %q, want the fixed user path %q", src.Path, want)
	}
	if !strings.Contains(string(src.Data), "gate.sh") {
		t.Fatalf("user config bytes not loaded; got %q", string(src.Data))
	}
	if strings.Contains(string(src.Data), "evil") {
		t.Fatalf("workspace config leaked into the hook source: %q", string(src.Data))
	}
}

func TestWorkspaceHooksStrippedWithWarning(t *testing.T) {
	home, ws := homeAndWorkspace(t)
	writeConfig(t, filepath.Join(home, ".mivia", "mivia.toml"), userHookTOML)
	wsPath := filepath.Join(ws, ".mivia", "mivia.toml")
	writeConfig(t, wsPath, workspaceHookTOML)

	src, err := LoadHooksSource(ws)
	if err != nil {
		t.Fatalf("LoadHooksSource: %v", err)
	}
	if len(src.Warnings) != 1 {
		t.Fatalf("want exactly one warning for the workspace file, got %d: %v", len(src.Warnings), src.Warnings)
	}
	w := src.Warnings[0]
	if !strings.Contains(w, wsPath) {
		t.Errorf("warning must name the ignored file %q, got %q", wsPath, w)
	}
	if !strings.Contains(w, "2") {
		t.Errorf("warning must name the ignored hook count (2), got %q", w)
	}
}

func TestWorkspaceWithoutHooksEmitsNoWarning(t *testing.T) {
	home, ws := homeAndWorkspace(t)
	writeConfig(t, filepath.Join(home, ".mivia", "mivia.toml"), userHookTOML)
	writeConfig(t, filepath.Join(ws, ".mivia", "mivia.toml"), "[provider]\nname = \"openai\"\n")

	src, err := LoadHooksSource(ws)
	if err != nil {
		t.Fatalf("LoadHooksSource: %v", err)
	}
	if len(src.Warnings) != 0 {
		t.Fatalf("a workspace config with no [[hooks]] must not warn, got %v", src.Warnings)
	}
}

func TestMiviaConfigEnvDoesNotRelocateHookSource(t *testing.T) {
	home, ws := homeAndWorkspace(t)
	writeConfig(t, filepath.Join(home, ".mivia", "mivia.toml"), userHookTOML)
	elsewhere := filepath.Join(ws, "elsewhere.toml")
	writeConfig(t, elsewhere, workspaceHookTOML)
	t.Setenv("MIVIA_CONFIG", elsewhere)

	src, err := LoadHooksSource(ws)
	if err != nil {
		t.Fatalf("LoadHooksSource: %v", err)
	}
	if src.Path != filepath.Join(home, ".mivia", "mivia.toml") {
		t.Fatalf("$MIVIA_CONFIG relocated the hook source to %q", src.Path)
	}
	if strings.Contains(string(src.Data), "evil") {
		t.Fatalf("$MIVIA_CONFIG file supplied hook bytes: %q", string(src.Data))
	}
	joined := strings.Join(src.Warnings, "\n")
	if !strings.Contains(joined, elsewhere) {
		t.Fatalf("an ignored $MIVIA_CONFIG hook table must warn, got %v", src.Warnings)
	}
}

func TestHooksSourceKeepsUserMeaningWhenWorkspaceIsHome(t *testing.T) {
	base := t.TempDir()
	t.Setenv("HOME", base)
	t.Setenv("MIVIA_CONFIG", "")
	writeConfig(t, filepath.Join(base, ".mivia", "mivia.toml"), userHookTOML)

	src, err := LoadHooksSource(base)
	if err != nil {
		t.Fatalf("LoadHooksSource: %v", err)
	}
	if !strings.Contains(string(src.Data), "gate.sh") {
		t.Fatalf("user config must still load when the workspace is home; got %q", string(src.Data))
	}
	if len(src.Warnings) != 0 {
		t.Fatalf("home-as-workspace must not warn about its own user file, got %v", src.Warnings)
	}
}

func TestHooksSourceRefusesSymlinkedUserConfig(t *testing.T) {
	home, ws := homeAndWorkspace(t)
	target := filepath.Join(ws, "planted.toml")
	writeConfig(t, target, workspaceHookTOML)
	link := filepath.Join(home, ".mivia", "mivia.toml")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	_, err := LoadHooksSource(ws)
	if err == nil {
		t.Fatal("a symlinked user config must be refused as a hook source, got nil error")
	}
	if !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("error must name the symlink refusal, got %v", err)
	}
}

// A cloned repo must not be able to choose how much memory startup allocates.
// os.ReadFile sizes its buffer from the file, so an oversized workspace config
// would be read whole before anything decided not to trust it.
func TestOversizedWorkspaceConfigIsBoundedAndReported(t *testing.T) {
	home, ws := homeAndWorkspace(t)
	writeConfig(t, filepath.Join(home, ".mivia", "mivia.toml"), userHookTOML)
	wsPath := filepath.Join(ws, ".mivia", "mivia.toml")
	writeConfig(t, wsPath, workspaceHookTOML+strings.Repeat("# pad\n", 400_000))
	if info, err := os.Stat(wsPath); err != nil || info.Size() <= maxHookConfigBytes {
		t.Fatalf("fixture must exceed the bound: size err=%v", err)
	}

	src, err := LoadHooksSource(ws)
	if err != nil {
		t.Fatalf("an oversized workspace config must not fail the load: %v", err)
	}
	joined := strings.Join(src.Warnings, "\n")
	if !strings.Contains(joined, "not inspected") || !strings.Contains(joined, wsPath) {
		t.Fatalf("an over-bound candidate must be reported, not silently skipped; got %v", src.Warnings)
	}
	if !strings.Contains(string(src.Data), "gate.sh") {
		t.Fatalf("the user hook source must still load; got %q", string(src.Data))
	}
}

func TestOversizedUserConfigIsRefused(t *testing.T) {
	home, ws := homeAndWorkspace(t)
	writeConfig(t, filepath.Join(home, ".mivia", "mivia.toml"),
		userHookTOML+strings.Repeat("# pad\n", 400_000))

	_, err := LoadHooksSource(ws)
	if err == nil {
		t.Fatal("an over-bound user config must be refused, got nil error")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("error must name the byte bound, got %v", err)
	}
}

func TestHooksSourceAbsentUserConfigIsNotAnError(t *testing.T) {
	_, ws := homeAndWorkspace(t)
	writeConfig(t, filepath.Join(ws, ".mivia", "mivia.toml"), workspaceHookTOML)

	src, err := LoadHooksSource(ws)
	if err != nil {
		t.Fatalf("absent user config must not fail the load: %v", err)
	}
	if src.Data != nil {
		t.Fatalf("absent user config must yield no hook bytes, got %q", string(src.Data))
	}
	if len(src.Warnings) != 1 {
		t.Fatalf("workspace hooks must still warn when no user config exists, got %v", src.Warnings)
	}
}
