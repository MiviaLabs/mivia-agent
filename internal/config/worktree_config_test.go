package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadWorktreeConfigDefaultsWhenConfigIsMissing(t *testing.T) {
	cfg, err := LoadWorktreeConfig(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cfg.BranchPrefix, DefaultWorktreeBranchPrefix; got != want {
		t.Fatalf("branch prefix = %q, want %q", got, want)
	}
}

func TestLoadWorktreeConfigUsesConfiguredPrefix(t *testing.T) {
	root := writeWorktreeConfig(t, "[worktrees]\nbranch_prefix = \"team/\"\n")
	cfg, err := LoadWorktreeConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cfg.BranchPrefix, "team/"; got != want {
		t.Fatalf("branch prefix = %q, want %q", got, want)
	}
}

func TestLoadWorktreeConfigIgnoresMiviaConfig(t *testing.T) {
	t.Setenv("MIVIA_CONFIG", writeMinimalConfig(t, "\n[worktrees]\nbranch_prefix = \"environment/\"\n"))
	root := writeWorktreeConfig(t, "[worktrees]\nbranch_prefix = \"target/\"\n")
	cfg, err := LoadWorktreeConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cfg.BranchPrefix, "target/"; got != want {
		t.Fatalf("branch prefix = %q, want %q", got, want)
	}
}

func TestLoadWorktreeConfigRejectsInvalidPrefix(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
		want   string
	}{
		{name: "empty", prefix: "", want: "must not be empty"},
		{name: "no trailing slash", prefix: "team", want: "must end with /"},
		{name: "invalid Git ref", prefix: "team..bad/", want: "not a valid Git ref"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := writeWorktreeConfig(t, "[worktrees]\nbranch_prefix = \""+tt.prefix+"\"\n")
			_, err := LoadWorktreeConfig(root)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func writeWorktreeConfig(t *testing.T, contents string) string {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, ".mivia", "mivia.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestLoadDefaultsWorktreeBranchPrefix(t *testing.T) {
	res, err := Load(LoadOptions{ConfigPath: writeMinimalConfig(t, "")})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := resolvedWorktreeBranchPrefix(t, res), "mivia/"; got != want {
		t.Fatalf("worktree branch prefix = %q, want %q", got, want)
	}
}

func TestLoadUsesConfiguredWorktreeBranchPrefix(t *testing.T) {
	res, err := Load(LoadOptions{ConfigPath: writeMinimalConfig(t, "\n[worktrees]\nbranch_prefix = \"team/\"\n")})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := resolvedWorktreeBranchPrefix(t, res), "team/"; got != want {
		t.Fatalf("worktree branch prefix = %q, want %q", got, want)
	}
}

func TestLoadWithExplicitConfigIgnoresMiviaConfigForWorktreePrefix(t *testing.T) {
	t.Setenv("MIVIA_CONFIG", writeMinimalConfig(t, "\n[worktrees]\nbranch_prefix = \"environment/\"\n"))
	res, err := Load(LoadOptions{ConfigPath: writeMinimalConfig(t, "\n[worktrees]\nbranch_prefix = \"target/\"\n")})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := resolvedWorktreeBranchPrefix(t, res), "target/"; got != want {
		t.Fatalf("worktree branch prefix = %q, want %q", got, want)
	}
}

// resolvedWorktreeBranchPrefix keeps the RED tests buildable before the new
// config surface exists. The test fails with an assertion until Load exposes
// the configured worktree prefix on Resolved.
func resolvedWorktreeBranchPrefix(t *testing.T, res *Resolved) string {
	t.Helper()
	value := reflect.ValueOf(res)
	if value.Kind() != reflect.Pointer || value.IsNil() {
		t.Fatal("Load returned no resolved config")
	}
	worktrees := value.Elem().FieldByName("Worktrees")
	if !worktrees.IsValid() {
		t.Fatal("Resolved.Worktrees is missing")
	}
	if worktrees.Kind() == reflect.Pointer {
		if worktrees.IsNil() {
			t.Fatal("Resolved.Worktrees is nil")
		}
		worktrees = worktrees.Elem()
	}
	prefix := worktrees.FieldByName("BranchPrefix")
	if !prefix.IsValid() || prefix.Kind() != reflect.String {
		t.Fatal("Resolved.Worktrees.BranchPrefix is missing")
	}
	return prefix.String()
}
