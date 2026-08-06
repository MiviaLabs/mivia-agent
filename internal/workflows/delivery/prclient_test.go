package delivery

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestParseOwnerRepo(t *testing.T) {
	good := []struct {
		in   string
		want string
	}{
		{"https://github.com/owner/repo", "owner/repo"},
		{"https://github.com/owner/repo.git", "owner/repo"},
		{"git@github.com:owner/repo", "owner/repo"},
		{"git@github.com:owner/repo.git", "owner/repo"},
		{"ssh://git@github.com/owner/repo", "owner/repo"},
		{"ssh://git@github.com/owner/repo.git", "owner/repo"},
		{"https://GitHub.com/Owner/Repo", "Owner/Repo"},
	}
	for _, tc := range good {
		got, err := ParseOwnerRepo(tc.in)
		if err != nil {
			t.Errorf("ParseOwnerRepo(%q) error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseOwnerRepo(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	bad := []string{
		"",
		"https://gitlab.com/owner/repo",
		"git@gitlab.com:owner/repo",
		"ssh://git@gitlab.com/owner/repo",
		"https://github.com//repo",
		"https://github.com/owner/",
		"https://github.com/owner",
		"https://github.com/owner/repo/sub",
		"https://github.com/owner/repo/sub.git",
		"git@github.com:owner/repo/sub",
		"https://github.com",
		"https://",
		"not a remote url",
		"https://github.com:443/owner/repo",
	}
	for _, tc := range bad {
		if got, err := ParseOwnerRepo(tc); err == nil {
			t.Errorf("ParseOwnerRepo(%q) = %q, want error", tc, got)
		}
	}
}

// writeFakeGH installs a fake gh executable on PATH. The script records
// its argv to $GH_ARGS_FILE and its environment to $GH_ENV_FILE. When
// $GH_EXIT is set, the script prints $GH_EXIT_MSG to stderr and exits
// with that code. Otherwise it prints $GH_STDOUT.
func writeFakeGH(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
printf '%s\n' "$@" > "$GH_ARGS_FILE"
env | sort > "$GH_ENV_FILE"
if [ -n "$GH_EXIT" ]; then
  printf '%s\n' "$GH_EXIT_MSG" >&2
  exit "$GH_EXIT"
fi
printf '%s' "$GH_STDOUT"
`
	path := filepath.Join(dir, "gh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func readRecordedArgs(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(os.Getenv("GH_ARGS_FILE"))
	if err != nil {
		t.Fatalf("read recorded args: %v", err)
	}
	text := strings.TrimSuffix(string(data), "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// assertGHEnv checks that the fake gh child ran with prompts disabled
// and without the GIT_* variables that the test pinned.
func assertGHEnv(t *testing.T) {
	t.Helper()
	data, err := os.ReadFile(os.Getenv("GH_ENV_FILE"))
	if err != nil {
		t.Fatalf("read recorded env: %v", err)
	}
	seenPrompt := false
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "GIT_DIR=") || strings.HasPrefix(line, "GIT_TERMINAL_PROMPT=") {
			t.Errorf("env leaks pinned variable %q", line)
		}
		if line == "GH_PROMPT_DISABLED=1" {
			seenPrompt = true
		}
	}
	if !seenPrompt {
		t.Error("env lacks GH_PROMPT_DISABLED=1")
	}
}

func TestGitHubCLIFindByHead(t *testing.T) {
	writeFakeGH(t)
	t.Setenv("GH_ARGS_FILE", filepath.Join(t.TempDir(), "args.txt"))
	t.Setenv("GH_ENV_FILE", filepath.Join(t.TempDir(), "env.txt"))
	t.Setenv("GIT_DIR", "/leaked/repo")
	t.Setenv("GIT_TERMINAL_PROMPT", "0")

	t.Run("found", func(t *testing.T) {
		t.Setenv("GH_STDOUT", `[{"number":12,"url":"https://github.com/o/r/pull/12","headRepositoryOwner":{"login":"owner"}}]`)
		got, err := (GitHubCLI{}).FindByHead(context.Background(), "owner/repo", "feature/x")
		if err != nil {
			t.Fatalf("FindByHead error: %v", err)
		}
		if got == nil {
			t.Fatal("FindByHead = nil, want PRRef")
		}
		if got.RemoteID != "12" || got.URL != "https://github.com/o/r/pull/12" {
			t.Errorf("FindByHead = %+v, want RemoteID 12 with PR url", got)
		}
		want := []string{"pr", "list", "--repo", "owner/repo", "--head", "feature/x", "--state", "all", "--json", "number,url,headRepositoryOwner"}
		if gotArgs := readRecordedArgs(t); !slices.Equal(gotArgs, want) {
			t.Errorf("argv = %q, want %q", gotArgs, want)
		}
		assertGHEnv(t)
	})

	t.Run("empty", func(t *testing.T) {
		t.Setenv("GH_STDOUT", "[]")
		got, err := (GitHubCLI{}).FindByHead(context.Background(), "owner/repo", "feature/x")
		if err != nil {
			t.Fatalf("FindByHead error: %v", err)
		}
		if got != nil {
			t.Errorf("FindByHead = %+v, want nil", got)
		}
	})

	t.Run("non-zero exit", func(t *testing.T) {
		t.Setenv("GH_EXIT", "3")
		t.Setenv("GH_EXIT_MSG", "boom: auth failed")
		_, err := (GitHubCLI{}).FindByHead(context.Background(), "owner/repo", "feature/x")
		if err == nil {
			t.Fatal("FindByHead error = nil, want error")
		}
		if !strings.Contains(err.Error(), "exit status") {
			t.Errorf("error %q does not mention exit status", err)
		}
		if !strings.Contains(err.Error(), "boom") {
			t.Errorf("error %q does not include stderr", err)
		}
	})

	t.Run("malformed json", func(t *testing.T) {
		t.Setenv("GH_STDOUT", "not json")
		if _, err := (GitHubCLI{}).FindByHead(context.Background(), "owner/repo", "feature/x"); err == nil {
			t.Fatal("FindByHead error = nil, want malformed JSON error")
		}
	})
}

// TestGitHubCLIFindByHeadScopesToRepoOwner: gh pr list --head matches by
// branch name across head repositories, so a fork PR with the same branch
// name must be skipped; only a PR whose head repository belongs to the
// target repository's owner may be reused as this delivery's PR.
func TestGitHubCLIFindByHeadScopesToRepoOwner(t *testing.T) {
	writeFakeGH(t)
	t.Setenv("GH_ARGS_FILE", filepath.Join(t.TempDir(), "args.txt"))
	t.Setenv("GH_ENV_FILE", filepath.Join(t.TempDir(), "env.txt"))

	t.Run("fork PR with same branch is skipped", func(t *testing.T) {
		t.Setenv("GH_STDOUT", `[{"number":1,"url":"https://github.com/other/repo/pull/1","headRepositoryOwner":{"login":"other"}},{"number":2,"url":"https://github.com/o/r/pull/2","headRepositoryOwner":{"login":"owner"}}]`)
		got, err := (GitHubCLI{}).FindByHead(context.Background(), "owner/repo", "feature/x")
		if err != nil {
			t.Fatalf("FindByHead error: %v", err)
		}
		if got == nil {
			t.Fatal("FindByHead = nil, want the owner PR")
		}
		if got.RemoteID != "2" || got.URL != "https://github.com/o/r/pull/2" {
			t.Errorf("FindByHead = %+v, want the owner's PR (fork PR skipped)", got)
		}
	})

	t.Run("only fork PRs returns nil", func(t *testing.T) {
		t.Setenv("GH_STDOUT", `[{"number":1,"url":"https://github.com/other/repo/pull/1","headRepositoryOwner":{"login":"other"}}]`)
		got, err := (GitHubCLI{}).FindByHead(context.Background(), "owner/repo", "feature/x")
		if err != nil {
			t.Fatalf("FindByHead error: %v", err)
		}
		if got != nil {
			t.Errorf("FindByHead = %+v, want nil (only a fork PR exists)", got)
		}
	})
}

func TestGitHubCLICreate(t *testing.T) {
	writeFakeGH(t)
	t.Setenv("GH_ARGS_FILE", filepath.Join(t.TempDir(), "args.txt"))
	t.Setenv("GH_ENV_FILE", filepath.Join(t.TempDir(), "env.txt"))
	t.Setenv("GH_STDOUT", `{"number":7,"url":"https://github.com/o/r/pull/7"}`)

	t.Run("draft", func(t *testing.T) {
		in := PRInput{
			Base:  "main",
			Head:  "feature/x",
			Title: "-fix: add tests",
			Body:  "-body starts with a dash",
			Draft: true,
		}
		got, err := (GitHubCLI{}).Create(context.Background(), "owner/repo", in)
		if err != nil {
			t.Fatalf("Create error: %v", err)
		}
		if got.RemoteID != "7" || got.URL != "https://github.com/o/r/pull/7" {
			t.Errorf("Create = %+v, want RemoteID 7 with PR url", got)
		}
		want := []string{
			"pr", "create",
			"--repo", "owner/repo",
			"--base", "main",
			"--head", "feature/x",
			"--title=-fix: add tests",
			"--body=-body starts with a dash",
			"--draft",
			"--json", "number,url",
		}
		if gotArgs := readRecordedArgs(t); !slices.Equal(gotArgs, want) {
			t.Errorf("argv = %q, want %q", gotArgs, want)
		}
		assertGHEnv(t)
	})

	t.Run("not draft", func(t *testing.T) {
		in := PRInput{Base: "main", Head: "feature/x", Title: "t", Body: "b"}
		if _, err := (GitHubCLI{}).Create(context.Background(), "owner/repo", in); err != nil {
			t.Fatalf("Create error: %v", err)
		}
		if gotArgs := readRecordedArgs(t); slices.Contains(gotArgs, "--draft") {
			t.Errorf("argv contains --draft for a non-draft PR: %q", gotArgs)
		}
	})

	t.Run("malformed json", func(t *testing.T) {
		t.Setenv("GH_STDOUT", "oops")
		in := PRInput{Base: "main", Head: "feature/x", Title: "t", Body: "b"}
		if _, err := (GitHubCLI{}).Create(context.Background(), "owner/repo", in); err == nil {
			t.Fatal("Create error = nil, want malformed JSON error")
		}
	})
}
