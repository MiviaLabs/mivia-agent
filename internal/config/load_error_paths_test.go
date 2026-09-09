package config

// loadFile's error-propagation paths. Each is a real failure an operator can
// hit: no config anywhere, a workspace overlay that cannot be read or parsed,
// and a workspace [verifiers] table that does not validate. They are driven
// through Load so the wiring between the two is covered too.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeConfigAt writes a config file, creating its parent directories.
func writeConfigAt(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

const minimalProviderTOML = `[provider]
name = "deepseek"

[providers.deepseek]
models = [{ name = "deepseek-v4-pro", context_window_tokens = 128000 }]
default_model = "deepseek-v4-pro"
`

// TestLoad_NoConfigAnywhereIsAnError pins loadFile's own "path is still
// empty" refusal: with no explicit ConfigPath, no candidate on disk, and
// neither AllowMissingConfig nor AutoBootstrapUserConfig set, the load must
// name what it looked for and what to create rather than return a silently
// empty config.
func TestLoad_NoConfigAnywhereIsAnError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MIVIA_CONFIG", "")
	t.Chdir(t.TempDir())

	_, err := Load(LoadOptions{WorkspaceRoot: t.TempDir()})
	if err == nil {
		t.Fatal("Load accepted a tree with no config file anywhere")
	}
	if !strings.Contains(err.Error(), "no config file found") {
		t.Fatalf("err = %v, want the no-config-found refusal", err)
	}
}

// TestLoad_UnparseableWorkspaceOverlaySurfaces pins the overlay decode
// error: the base config is fine, but the workspace's own .mivia/mivia.toml
// is not valid TOML. Ignoring it would silently drop every workspace-local
// override the operator wrote.
func TestLoad_UnparseableWorkspaceOverlaySurfaces(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MIVIA_CONFIG", "")

	base := filepath.Join(t.TempDir(), "base.toml")
	writeConfigAt(t, base, minimalProviderTOML)

	ws := t.TempDir()
	writeConfigAt(t, filepath.Join(ws, ".mivia", "mivia.toml"), "this is not = = valid toml\n")

	_, err := Load(LoadOptions{ConfigPath: base, WorkspaceRoot: ws})
	if err == nil {
		t.Fatal("Load accepted a workspace overlay it could not parse")
	}
}

// TestLoad_UnreadableWorkspaceOverlaySurfaces pins the overlay ReadFile
// error, which is distinct from the decode error above: the file passes
// workspaceOverlayConfigPath's regular-file check, then fails to open.
func TestLoad_UnreadableWorkspaceOverlaySurfaces(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MIVIA_CONFIG", "")

	base := filepath.Join(t.TempDir(), "base.toml")
	writeConfigAt(t, base, minimalProviderTOML)

	ws := t.TempDir()
	overlay := filepath.Join(ws, ".mivia", "mivia.toml")
	writeConfigAt(t, overlay, minimalProviderTOML)
	if err := os.Chmod(overlay, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(overlay, 0o600) })
	if _, probeErr := os.ReadFile(overlay); probeErr == nil {
		t.Skip("platform still reads a 0000 file")
	}

	_, err := Load(LoadOptions{ConfigPath: base, WorkspaceRoot: ws})
	if err == nil {
		t.Fatal("Load accepted a workspace overlay it could not read")
	}
	if !strings.Contains(err.Error(), "read workspace config") {
		t.Fatalf("err = %v, want the read-workspace-config wrap", err)
	}
}

// TestLoad_InvalidWorkspaceVerifiersSurfaces pins the LoadWorkspaceVerifiers
// error. The workspace file is the BASE here, so the overlay branch is
// skipped (workspaceOverlayConfigPath refuses candidate == basePath) and the
// file decodes as a File fine - only the [verifiers] layer rejects it. A
// silently dropped profile would let an evidence gate pass by doing nothing.
func TestLoad_InvalidWorkspaceVerifiersSurfaces(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MIVIA_CONFIG", "")

	ws := t.TempDir()
	wsConfig := filepath.Join(ws, ".mivia", "mivia.toml")
	writeConfigAt(t, wsConfig, minimalProviderTOML+`
[verifiers.repository]
bogus_key = 1
`)

	_, err := Load(LoadOptions{ConfigPath: wsConfig, WorkspaceRoot: ws})
	if err == nil {
		t.Fatal("Load accepted a [verifiers] table with an unknown key")
	}
	if !strings.Contains(err.Error(), "unknown key") {
		t.Fatalf("err = %v, want the unknown-key rejection", err)
	}
}

// TestLoad_BootstrapFailureSurfaces pins loadFile's `case err != nil` arm on
// the auto-bootstrap switch, distinct from the errUserConfigExists arm above
// it. A regular file sitting where the ~/.mivia DIRECTORY belongs makes the
// bootstrap's own stat fail with ENOTDIR rather than ENOENT, so it is
// neither "already exists" nor "absent, go create it" - and that error must
// reach the caller instead of being flattened into a missing-config message.
func TestLoad_BootstrapFailureSurfaces(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MIVIA_CONFIG", "")
	t.Chdir(t.TempDir())

	// A FILE, not a directory, at ~/.mivia.
	if err := os.WriteFile(filepath.Join(home, ".mivia"), []byte("not a directory\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Load(LoadOptions{
		WorkspaceRoot:           t.TempDir(),
		AutoBootstrapUserConfig: true,
		AllowMissingConfig:      true,
	})
	if err == nil {
		t.Fatal("Load hid a bootstrap failure behind AllowMissingConfig")
	}
	if !strings.Contains(err.Error(), "auto-bootstrap user config") {
		t.Fatalf("err = %v, want the auto-bootstrap wrap", err)
	}
}
