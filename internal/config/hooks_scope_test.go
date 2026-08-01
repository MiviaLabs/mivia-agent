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

// hasHookBytes reports whether any loaded config carries the given text.
func hasHookBytes(src HooksSource, want string) bool {
	for _, file := range src.Files {
		if strings.Contains(string(file.Data), want) {
			return true
		}
	}
	return false
}

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

// Both surfaces load, and they ADD. A workspace file that replaced the user's
// would silently disarm a global gate by opening a repository.
func TestHooksLoadFromBothUserAndProjectConfigs(t *testing.T) {
	home, ws := homeAndWorkspace(t)
	writeConfig(t, filepath.Join(home, ".mivia", "mivia.toml"), userHookTOML)
	writeConfig(t, filepath.Join(ws, ".mivia", "mivia.toml"), workspaceHookTOML)

	src, err := LoadHooksSource(ws)
	if err != nil {
		t.Fatalf("LoadHooksSource: %v", err)
	}
	if len(src.Files) != 2 {
		t.Fatalf("want both configs loaded, got %d: %+v", len(src.Files), src.Files)
	}
	// Order is load-bearing: PreToolUse stops at the first deny, so the user's
	// own gates must answer before a repository's do.
	if src.Files[0].Project {
		t.Fatal("the user config must come first so its gates answer first")
	}
	if !strings.Contains(string(src.Files[0].Data), "gate.sh") {
		t.Fatalf("user config bytes not loaded; got %q", src.Files[0].Data)
	}
	if !src.Files[1].Project {
		t.Fatal("the workspace config must be marked as a project source")
	}
	if !strings.Contains(string(src.Files[1].Data), "evil.sh") {
		t.Fatalf("workspace config bytes not loaded; got %q", src.Files[1].Data)
	}
	if len(src.Warnings) != 0 {
		t.Fatalf("a loaded project config must not also warn, got %v", src.Warnings)
	}
}

// Project hooks are not conditional on the user having any.
func TestProjectHooksLoadWithNoUserConfigAtAll(t *testing.T) {
	_, ws := homeAndWorkspace(t)
	writeConfig(t, filepath.Join(ws, ".mivia", "mivia.toml"), workspaceHookTOML)

	src, err := LoadHooksSource(ws)
	if err != nil {
		t.Fatalf("LoadHooksSource: %v", err)
	}
	if len(src.Files) != 1 || !src.Files[0].Project {
		t.Fatalf("the workspace config must load on its own, got %+v", src.Files)
	}
}

// A repository must not be able to point the hook source at a file outside
// itself, nor break every session in its own directory.
func TestProjectHookConfigFailsSoftAndRefusesLinks(t *testing.T) {
	home, ws := homeAndWorkspace(t)
	writeConfig(t, filepath.Join(home, ".mivia", "mivia.toml"), userHookTOML)
	target := filepath.Join(ws, "elsewhere.toml")
	writeConfig(t, target, workspaceHookTOML)
	link := filepath.Join(ws, ".mivia", "mivia.toml")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	src, err := LoadHooksSource(ws)
	if err != nil {
		t.Fatalf("a link-shaped project config must not fail startup: %v", err)
	}
	for _, file := range src.Files {
		if file.Project {
			t.Fatal("a symlinked project config supplied hooks")
		}
	}
	if !strings.Contains(strings.Join(src.Warnings, "\n"), link) {
		t.Fatalf("the refused project config must be named, got %v", src.Warnings)
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
	for _, file := range src.Files {
		if sameFilePath(file.Path, elsewhere) {
			t.Fatalf("$MIVIA_CONFIG supplied hook bytes from %q", file.Path)
		}
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
	if !hasHookBytes(src, "gate.sh") {
		t.Fatalf("user config must still load when the workspace is home; got %+v", src.Files)
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
	if !hasHookBytes(src, "gate.sh") {
		t.Fatalf("the user hook source must still load; got %+v", src.Files)
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
	for _, file := range src.Files {
		if !file.Project {
			t.Fatalf("absent user config must yield no user hook bytes, got %q", file.Path)
		}
	}
	if len(src.Warnings) != 0 {
		t.Fatalf("a loaded project config must not warn, got %v", src.Warnings)
	}
}
