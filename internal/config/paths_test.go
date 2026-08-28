package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

func TestDefaultConfigCandidatesUsesNamespace(t *testing.T) {
	t.Setenv("MIVIA_CONFIG", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		workspace.NamespacePath(cwd, "mivia.toml"),
		workspace.NamespacePath(home, "mivia.toml"),
	}
	if got := DefaultConfigCandidates(); !equalStrings(got, want) {
		t.Fatalf("config candidates = %v, want %v", got, want)
	}
}

func TestDefaultConfigCandidatesHasNoLegacyUserPath(t *testing.T) {
	t.Setenv("MIVIA_CONFIG", "")
	t.Setenv("HOME", t.TempDir())
	for _, candidate := range DefaultConfigCandidates() {
		if strings.Contains(candidate, filepath.Join(".config", "mivia")) {
			t.Errorf("legacy user config path must not be searched: %q", candidate)
		}
	}
}

func TestCandidateOrderPrefersWorkspaceOverUser(t *testing.T) {
	workspaceRoot := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MIVIA_CONFIG", "")
	t.Chdir(workspaceRoot)

	workspaceConfig := workspace.NamespacePath(workspaceRoot, "mivia.toml")
	userConfig := workspace.NamespacePath(home, "mivia.toml")
	if err := os.MkdirAll(filepath.Dir(workspaceConfig), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(userConfig), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workspaceConfig, []byte("workspace"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userConfig, []byte("user"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, ok := FirstExisting(DefaultConfigCandidates())
	if !ok || got != workspaceConfig {
		t.Fatalf("first config = %q, %t; want workspace %q", got, ok, workspaceConfig)
	}
}

func TestDefaultEnvCandidatesUsesNamespace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	want := []string{filepath.Join(cwd, ".env"), workspace.NamespacePath(home, ".env")}
	if got := DefaultEnvCandidates(); !equalStrings(got, want) {
		t.Fatalf("env candidates = %v, want %v", got, want)
	}
	if got := DefaultEnvCandidates()[0]; got == filepath.Join(cwd, workspace.Namespace) {
		t.Fatalf("workspace env must remain at repository root, got %q", got)
	}
}

func TestUserConfigPath(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home-does-not-exist")
	t.Setenv("HOME", home)
	if got, want := UserConfigPath(), workspace.NamespacePath(home, "mivia.toml"); got != want {
		t.Fatalf("user config path = %q, want %q", got, want)
	}
}

func TestUserEnvPath(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home-does-not-exist")
	t.Setenv("HOME", home)
	if got, want := UserEnvPath(), workspace.NamespacePath(home, ".env"); got != want {
		t.Fatalf("user env path = %q, want %q", got, want)
	}
}

func TestUserAuthPath(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home-does-not-exist")
	t.Setenv("HOME", home)
	if got, want := UserAuthPath(), workspace.NamespacePath(home, "auth.json"); got != want {
		t.Fatalf("user auth path = %q, want %q", got, want)
	}
}

func TestDefaultConfigCandidatesHonorsEnvOverrideFirst(t *testing.T) {
	t.Setenv("MIVIA_CONFIG", "/tmp/explicit.toml")
	got := DefaultConfigCandidates()
	if len(got) == 0 || got[0] != "/tmp/explicit.toml" {
		t.Fatalf("MIVIA_CONFIG must win: %v", got)
	}
}

func TestProjectConfigExists(t *testing.T) {
	t.Run("true when the project config file exists", func(t *testing.T) {
		root := t.TempDir()
		dir := filepath.Join(root, ".mivia")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "mivia.toml"), []byte(""), 0o600); err != nil {
			t.Fatal(err)
		}
		if !ProjectConfigExists(root) {
			t.Fatal("expected true when <root>/.mivia/mivia.toml exists as a regular file")
		}
	})

	t.Run("false when the directory exists but the file does not", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, ".mivia"), 0o700); err != nil {
			t.Fatal(err)
		}
		if ProjectConfigExists(root) {
			t.Fatal("expected false when the .mivia directory exists but mivia.toml does not")
		}
	})

	t.Run("false when root does not exist", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "does-not-exist")
		if ProjectConfigExists(root) {
			t.Fatal("expected false when root does not exist")
		}
	})

	t.Run("false when root is empty", func(t *testing.T) {
		if ProjectConfigExists("") {
			t.Fatal("expected false when root is empty")
		}
	})
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
