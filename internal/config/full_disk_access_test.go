package config

import (
	"os"
	"path/filepath"
	"testing"
)

// writeFullDiskUserConfig seeds a user-level config under $HOME (the
// uncached HOME override is the established seam - see
// memory_config_test.go's writeUserMemoryConfig) and returns its path.
func writeFullDiskUserConfig(t *testing.T, body string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := UserConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestUserFullDiskAccessDefaultsFalse pins fail-closed: no user config and
// no key means no grant. Full disk is a deliberate operator act, never a
// default.
func TestUserFullDiskAccessDefaultsFalse(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if UserFullDiskAccessForWorkspace("") {
		t.Fatal("full disk reported on with no user config at all")
	}
}

// TestUserFullDiskAccessReadsUserConfigOnly pins the positive provenance:
// the operator's own user config grants it.
func TestUserFullDiskAccessReadsUserConfigOnly(t *testing.T) {
	writeFullDiskUserConfig(t, "[workspace_access]\nfull_disk = true\n")
	if !UserFullDiskAccessForWorkspace("") {
		t.Fatal("full disk reported off despite the operator user config granting it")
	}
}

// TestUserFullDiskAccessIgnoresWorkspaceConfig is THE cloned-repo pinning
// test (audit F1/AR-1): a repository's own .mivia/mivia.toml claiming
// full_disk must never lift confinement, even though loadFile would happily
// merge that file's OTHER keys. The key lives outside File/Resolved by
// design; this test fails the moment anyone adds it back to the overlay
// path.
func TestUserFullDiskAccessIgnoresWorkspaceConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ws := t.TempDir()
	wsConfig := filepath.Join(ws, ".mivia", "mivia.toml")
	if err := os.MkdirAll(filepath.Dir(wsConfig), 0o700); err != nil {
		t.Fatal(err)
	}
	body := "[workspace_access]\nfull_disk = true\n"
	if err := os.WriteFile(wsConfig, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if UserFullDiskAccessForWorkspace(ws) {
		t.Fatal("workspace .mivia/mivia.toml granted full disk; a cloned repo could self-grant unconfined file tools")
	}
}

// TestUserFullDiskAccessRefusesHomeAsWorkspace pins the same-file read
// guard: when the user config path IS the workspace's config (repo planted
// at $HOME), the file is repo-controlled and must not answer.
func TestUserFullDiskAccessRefusesHomeAsWorkspace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	homeConfig := filepath.Join(home, ".mivia", "mivia.toml")
	if err := os.MkdirAll(filepath.Dir(homeConfig), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(homeConfig, []byte("[workspace_access]\nfull_disk = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if UserFullDiskAccessForWorkspace(home) {
		t.Fatal("user config read as authoritative while it IS the workspace's own config")
	}
}

// TestUserFullDiskAccessToleratesMalformedFile pins fail-closed on an
// unparsable user config: a broken file never becomes a grant.
func TestUserFullDiskAccessToleratesMalformedFile(t *testing.T) {
	writeFullDiskUserConfig(t, "[workspace_access\nfull_disk ===\n")
	if UserFullDiskAccessForWorkspace("") {
		t.Fatal("malformed user config produced a full-disk grant")
	}
}

// TestSetUserFullDiskAccessRoundTripAndPreservesOtherKeys covers the write
// path: set true, read back true; other keys survive the read-modify-write;
// set false, read back false.
func TestSetUserFullDiskAccessRoundTripAndPreservesOtherKeys(t *testing.T) {
	path := writeFullDiskUserConfig(t, "[tui]\ntheme = \"mivia-dark\"\nmouse = false\n")
	ws := t.TempDir() // unrelated workspace: the guard must not trip

	if err := SetUserFullDiskAccess(ws, true); err != nil {
		t.Fatalf("SetUserFullDiskAccess(true): %v", err)
	}
	if !UserFullDiskAccessForWorkspace(ws) {
		t.Fatal("full disk off after SetUserFullDiskAccess(true)")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"theme", "mivia-dark", "mouse", "workspace_access", "full_disk"} {
		if !contains(string(raw), want) {
			t.Fatalf("user config lost %q after write:\n%s", want, raw)
		}
	}

	if err := SetUserFullDiskAccess(ws, false); err != nil {
		t.Fatalf("SetUserFullDiskAccess(false): %v", err)
	}
	if UserFullDiskAccessForWorkspace(ws) {
		t.Fatal("full disk on after SetUserFullDiskAccess(false)")
	}
}

// TestSetUserFullDiskAccessRefusesWorkspaceFile pins the write-side guard
// (audit F2's flip side): when the user config resolves to the workspace's
// own file, the toggle must refuse and leave the file untouched - the
// setting must never land in a committable, repo-distributed file.
func TestSetUserFullDiskAccessRefusesWorkspaceFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	homeConfig := filepath.Join(home, ".mivia", "mivia.toml")
	if err := os.MkdirAll(filepath.Dir(homeConfig), 0o700); err != nil {
		t.Fatal(err)
	}
	original := "[tui]\ntheme = \"mivia-dark\"\n"
	if err := os.WriteFile(homeConfig, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SetUserFullDiskAccess(home, true); err == nil {
		t.Fatal("SetUserFullDiskAccess wrote into the workspace's own config file")
	}
	raw, err := os.ReadFile(homeConfig)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != original {
		t.Fatalf("refused write still modified the file:\n%s", raw)
	}
}

// TestFullDiskKeyInvisibleToConfigLoad pins AR-1's structural requirement:
// the [workspace_access] table must decode cleanly through File (no
// DisallowUnknownFields surprise for the user) while staying OFF the merged
// config surface - Load must succeed, and nothing about the key may reach
// the resolved config.
func TestFullDiskKeyInvisibleToConfigLoad(t *testing.T) {
	path := writeFullDiskUserConfig(t, `[provider]
name = "deepseek"

[providers.deepseek]
models = [{ name = "deepseek-v4-flash", context_window_tokens = 128000 }]

[workspace_access]
full_disk = true
`)
	if _, err := Load(LoadOptions{ConfigPath: path}); err != nil {
		t.Fatalf("Load rejected a user config carrying [workspace_access]: %v", err)
	}
}

// TestUserFullDiskAccessRefusesCwdAsWorkspace pins the relative-root hole
// (bug-audit ec8a9ef4 a3-1/a2-1): launched with cwd == $HOME and no
// --workspace, the pre-enterChatWorkspace call passes "" - the guard must
// still trip (EvalSymlinks keeps relative input relative, so without
// canonicalization abs-vs-rel never matched and a repo-planted
// $HOME/.mivia/mivia.toml answered the grant question).
func TestUserFullDiskAccessRefusesCwdAsWorkspace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	homeConfig := filepath.Join(home, ".mivia", "mivia.toml")
	if err := os.MkdirAll(filepath.Dir(homeConfig), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(homeConfig, []byte("[workspace_access]\nfull_disk = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(home); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(wd)
	if UserFullDiskAccessForWorkspace("") {
		t.Fatal("empty (cwd-relative) root granted full disk while the user config IS the workspace's own config")
	}
	if UserFullDiskAccessForWorkspace(".") {
		t.Fatal("explicit relative root granted full disk from home-as-workspace")
	}
}

// TestSetUserFullDiskAccessRefusesCwdAsWorkspace pins the write-side twin
// (bug-audit ec8a9ef4 a3-2): the same relative-root resolution must refuse
// the write, not persist the grant into the workspace-owned file.
func TestSetUserFullDiskAccessRefusesCwdAsWorkspace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	homeConfig := filepath.Join(home, ".mivia", "mivia.toml")
	if err := os.MkdirAll(filepath.Dir(homeConfig), 0o700); err != nil {
		t.Fatal(err)
	}
	original := "[tui]\ntheme = \"mivia-dark\"\n"
	if err := os.WriteFile(homeConfig, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(home); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(wd)
	if err := SetUserFullDiskAccess("", true); err == nil {
		t.Fatal("empty (cwd-relative) root wrote the grant into the workspace-owned config")
	}
	raw, err := os.ReadFile(homeConfig)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != original {
		t.Fatalf("refused write still modified the file:\n%s", raw)
	}
}

func contains(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
