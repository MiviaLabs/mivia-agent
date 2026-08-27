package delivery

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
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
// its argv to $GH_ARGS_FILE (pr create/list) or $GH_API_ARGS_FILE (api) and
// its environment to $GH_ENV_FILE. When $GH_EXIT is set, the script prints
// $GH_EXIT_MSG to stderr and exits with that code. Otherwise it prints
// $GH_STDOUT, except for `api` which prints $GH_STDOUT_API (default: a
// pulls payload carrying base.sha).
//
// The double rejects any argv mentioning baseRefOid exactly as released gh
// does. gh 2.46 has no such field on pr list or pr view and fails with
// "Unknown JSON field". A double that accepted it let a delivery-breaking
// regression ship green, so faithfulness here is the point, not pedantry.
//
// It is faithful on the `api` subcommand too: real gh declares
// `Args: cobra.ExactArgs(1)` (one positional endpoint) and refuses anything
// else with "accepts 1 arg(s), received N". The double enforces the same
// shape, so a doubled endpoint argument - which a permissive double accepts
// and a real gh rejects (DC-14) - fails the tests exactly as it fails in
// production.
func writeFakeGH(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	scheme := fakeGHScript()
	name := "gh"
	if runtime.GOOS == "windows" {
		name = "gh.cmd"
		scheme = fakeGHScriptWindows()
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(scheme), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// fakeGHScript returns the POSIX fake gh double. See the contract in the
// writeFakeGH doc comment; the Windows translation lives in
// prclient_fakegh_windows_test.go.
func fakeGHScript() string {
	return `#!/bin/sh
for arg in "$@"; do
  case "$arg" in
    *baseRefOid*)
      printf 'Unknown JSON field: "baseRefOid"\nAvailable fields:\n  additions\n' >&2
      exit 1
      ;;
  esac
done
if [ "$1" = "api" ]; then
  if [ "$#" -ne 2 ]; then
    printf 'accepts 1 arg(s), received %d\n' "$(($# - 1))" >&2
    exit 1
  fi
  printf '%s\n' "$@" > "${GH_API_ARGS_FILE:-/dev/null}"
  if [ -n "$GH_API_EXIT" ]; then
    printf '%s\n' "${GH_API_EXIT_MSG:-gh api failed}" >&2
    exit "$GH_API_EXIT"
  fi
  if [ -n "$GH_STDOUT_API" ]; then
    printf '%s' "$GH_STDOUT_API"
  else
    printf '%s' '{"base":{"sha":"1111111111111111111111111111111111111111"}}'
  fi
  exit 0
fi
printf '%s\n' "$@" > "$GH_ARGS_FILE"
env | sort > "$GH_ENV_FILE"
if [ -n "$GH_EXIT" ]; then
  printf '%s\n' "$GH_EXIT_MSG" >&2
  exit "$GH_EXIT"
fi
printf '%s' "$GH_STDOUT"
`
}

func readRecordedArgs(t *testing.T) []string {
	t.Helper()
	return readRecordedFileArgs(t, "GH_ARGS_FILE")
}

func readRecordedAPIArgs(t *testing.T) []string {
	t.Helper()
	return readRecordedFileArgs(t, "GH_API_ARGS_FILE")
}

func readRecordedFileArgs(t *testing.T, envName string) []string {
	t.Helper()
	data, err := os.ReadFile(os.Getenv(envName))
	if err != nil {
		t.Fatalf("read recorded args (%s): %v", envName, err)
	}
	text := strings.TrimSuffix(string(data), "\n")
	if text == "" {
		return nil
	}
	if runtime.GOOS == "windows" {
		// The gh.cmd double records argv space-joined (batch cannot split
		// arguments onto separate lines without re-splitting comma lists
		// like --json a,b). cmd.exe preserves the quoting exec.Command
		// applied, so a quote-aware split recovers the same argv.
		return splitQuotedFields(text)
	}
	return strings.Split(text, "\n")
}

// splitQuotedFields splits text into fields on whitespace, honoring double
// quotes (which are dropped), so an argv recorded by cmd.exe as %* parses
// back into the original arguments even when one of them contains spaces.
func splitQuotedFields(text string) []string {
	text = strings.TrimSpace(text) // echo %* appends CRLF
	var fields []string
	var cur strings.Builder
	inQuote := false
	for i := 0; i < len(text); i++ {
		switch c := text[i]; {
		case c == '"':
			inQuote = !inQuote
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			if !inQuote {
				if cur.Len() > 0 {
					fields = append(fields, cur.String())
					cur.Reset()
				}
				continue
			}
			cur.WriteByte(c)
		default:
			cur.WriteByte(c)
		}
	}
	if cur.Len() > 0 {
		fields = append(fields, cur.String())
	}
	return fields
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
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
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
		t.Setenv("GH_STDOUT", `[{"number":12,"url":"https://github.com/o/r/pull/12","isDraft":true,"headRepositoryOwner":{"login":"owner"}}]`)
		t.Setenv("GH_STDOUT_API", `{"base":{"sha":"aaa111"}}`)
		got, err := (GitHubCLI{}).FindByHead(context.Background(), "owner/repo", "feature/x")
		if err != nil {
			t.Fatalf("FindByHead error: %v", err)
		}
		if got == nil {
			t.Fatal("FindByHead = nil, want PRRef")
		}
		if got.RemoteID != "12" || got.URL != "https://github.com/o/r/pull/12" || !got.Draft || got.BaseRefOID != "aaa111" {
			t.Errorf("FindByHead = %+v, want RemoteID 12 with PR url, Draft=true, BaseRefOID aaa111", got)
		}
		want := []string{"pr", "list", "--repo", "owner/repo", "--head", "feature/x", "--state", "open", "--json", "number,url,title,isDraft,headRepositoryOwner"}
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

	t.Run("ready PR returned", func(t *testing.T) {
		t.Setenv("GH_STDOUT", `[{"number":12,"url":"https://github.com/o/r/pull/12","isDraft":false,"headRepositoryOwner":{"login":"owner"}}]`)
		got, err := (GitHubCLI{}).FindByHead(context.Background(), "owner/repo", "feature/x")
		if err != nil {
			t.Fatalf("FindByHead error: %v", err)
		}
		if got == nil || got.Draft {
			t.Errorf("FindByHead = %+v, want ready PR", got)
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
// TestGitHubCLIFindByHeadBaseOIDFailureSurfaces covers the base-commit lookup
// error path. A matching PR whose base commit cannot be resolved must surface
// the failure: returning the PR with an empty BaseRefOID would let delivery
// proceed against an unknown base, which is the condition INV-DUR-1 pins.
func TestGitHubCLIFindByHeadBaseOIDFailureSurfaces(t *testing.T) {
	writeFakeGH(t)
	t.Setenv("GH_ARGS_FILE", filepath.Join(t.TempDir(), "args.txt"))
	t.Setenv("GH_ENV_FILE", filepath.Join(t.TempDir(), "env.txt"))
	t.Setenv("GH_STDOUT", `[{"number":12,"url":"https://github.com/o/r/pull/12","isDraft":true,"headRepositoryOwner":{"login":"owner"}}]`)
	t.Setenv("GH_API_EXIT", "1")
	t.Setenv("GH_API_EXIT_MSG", "gh api: rate limited")

	got, err := (GitHubCLI{}).FindByHead(context.Background(), "owner/repo", "feature/x")
	if err == nil {
		t.Fatalf("FindByHead = %+v, want the base OID error", got)
	}
	if got != nil {
		t.Errorf("FindByHead returned %+v alongside an error, want nil", got)
	}
	if !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("error = %v, want it to carry the gh api failure", err)
	}
}

func TestGitHubCLIFindByHeadScopesToRepoOwner(t *testing.T) {
	writeFakeGH(t)
	t.Setenv("GH_ARGS_FILE", filepath.Join(t.TempDir(), "args.txt"))
	t.Setenv("GH_ENV_FILE", filepath.Join(t.TempDir(), "env.txt"))

	t.Run("fork PR with same branch is skipped", func(t *testing.T) {
		t.Setenv("GH_STDOUT", `[{"number":1,"url":"https://github.com/other/repo/pull/1","isDraft":true,"headRepositoryOwner":{"login":"other"}},{"number":2,"url":"https://github.com/o/r/pull/2","isDraft":true,"headRepositoryOwner":{"login":"owner"}}]`)
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

// TestGitHubCLIFindByHeadCaseInsensitiveOwner: GitHub owner names are
// case-insensitive. The remote slug may be lower-case while the API returns
// mixed case. The PR must still be found so delivery does not try to create a
// duplicate.
func TestGitHubCLIFindByHeadCaseInsensitiveOwner(t *testing.T) {
	writeFakeGH(t)
	t.Setenv("GH_ARGS_FILE", filepath.Join(t.TempDir(), "args.txt"))
	t.Setenv("GH_ENV_FILE", filepath.Join(t.TempDir(), "env.txt"))
	t.Setenv("GH_STDOUT", `[{"number":12,"url":"https://github.com/MiviaLabs/mivia-agent/pull/12","isDraft":false,"headRepositoryOwner":{"login":"MiviaLabs"}}]`)

	got, err := (GitHubCLI{}).FindByHead(context.Background(), "mivialabs/mivia-agent", "feature/x")
	if err != nil {
		t.Fatalf("FindByHead error: %v", err)
	}
	if got == nil {
		t.Fatal("FindByHead = nil, want PRRef")
	}
	if got.RemoteID != "12" || got.URL != "https://github.com/MiviaLabs/mivia-agent/pull/12" {
		t.Errorf("FindByHead = %+v, want RemoteID 12 with PR url", got)
	}
}

func TestGitHubCLIIsMerged(t *testing.T) {
	writeFakeGH(t)
	t.Setenv("GH_ARGS_FILE", filepath.Join(t.TempDir(), "args.txt"))
	t.Setenv("GH_ENV_FILE", filepath.Join(t.TempDir(), "env.txt"))

	t.Run("merged", func(t *testing.T) {
		t.Setenv("GH_STDOUT", `[{"state":"MERGED","mergedAt":"2026-08-16T12:00:00Z","headRepositoryOwner":{"login":"owner"}}]`)
		merged, err := (GitHubCLI{}).IsMerged(context.Background(), "owner/repo", "feature/x")
		if err != nil {
			t.Fatalf("IsMerged error: %v", err)
		}
		if !merged {
			t.Fatal("IsMerged = false, want true")
		}
		want := []string{"pr", "list", "--repo", "owner/repo", "--head", "feature/x", "--state", "all", "--json", "state,mergedAt,headRepositoryOwner"}
		if got := readRecordedArgs(t); !slices.Equal(got, want) {
			t.Errorf("argv = %q, want %q", got, want)
		}
	})

	t.Run("closed", func(t *testing.T) {
		t.Setenv("GH_STDOUT", `[{"state":"CLOSED","headRepositoryOwner":{"login":"owner"}}]`)
		merged, err := (GitHubCLI{}).IsMerged(context.Background(), "owner/repo", "feature/x")
		if err != nil {
			t.Fatalf("IsMerged error: %v", err)
		}
		if merged {
			t.Fatal("IsMerged = true, want false for closed PR")
		}
	})

	t.Run("open", func(t *testing.T) {
		t.Setenv("GH_STDOUT", `[{"state":"OPEN","headRepositoryOwner":{"login":"owner"}}]`)
		merged, err := (GitHubCLI{}).IsMerged(context.Background(), "owner/repo", "feature/x")
		if err != nil {
			t.Fatalf("IsMerged error: %v", err)
		}
		if merged {
			t.Fatal("IsMerged = true, want false for open PR")
		}
	})

	t.Run("fork PR skipped", func(t *testing.T) {
		t.Setenv("GH_STDOUT", `[{"state":"MERGED","headRepositoryOwner":{"login":"other"}}]`)
		merged, err := (GitHubCLI{}).IsMerged(context.Background(), "owner/repo", "feature/x")
		if err != nil {
			t.Fatalf("IsMerged error: %v", err)
		}
		if merged {
			t.Fatal("IsMerged = true, want false for fork PR")
		}
	})

	t.Run("no PR", func(t *testing.T) {
		t.Setenv("GH_STDOUT", `[]`)
		merged, err := (GitHubCLI{}).IsMerged(context.Background(), "owner/repo", "feature/x")
		if err != nil {
			t.Fatalf("IsMerged error: %v", err)
		}
		if merged {
			t.Fatal("IsMerged = true, want false when no PR exists")
		}
	})

	t.Run("gh error", func(t *testing.T) {
		t.Setenv("GH_EXIT", "1")
		t.Setenv("GH_EXIT_MSG", "api rate limited")
		_, err := (GitHubCLI{}).IsMerged(context.Background(), "owner/repo", "feature/x")
		if err == nil {
			t.Fatal("IsMerged error = nil, want gh failure")
		}
	})
}

func TestGitHubCLICreate(t *testing.T) {
	writeFakeGH(t)
	t.Setenv("GH_ARGS_FILE", filepath.Join(t.TempDir(), "args.txt"))
	t.Setenv("GH_ENV_FILE", filepath.Join(t.TempDir(), "env.txt"))
	t.Setenv("GH_STDOUT", `https://github.com/owner/repo/pull/7`+"\n")

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
		if got.RemoteID != "7" || got.URL != "https://github.com/owner/repo/pull/7" || got.BaseRefOID != "1111111111111111111111111111111111111111" {
			t.Errorf("Create = %+v, want RemoteID 7 with PR url and the baseRefOid from pr view", got)
		}
		want := []string{
			"pr", "create",
			"--repo", "owner/repo",
			"--base", "main",
			"--head", "feature/x",
			"--title=-fix: add tests",
			"--body=-body starts with a dash",
			"--draft",
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

	t.Run("no --json flag in argv", func(t *testing.T) {
		in := PRInput{Base: "main", Head: "feature/x", Title: "t", Body: "b"}
		if _, err := (GitHubCLI{}).Create(context.Background(), "owner/repo", in); err != nil {
			t.Fatalf("Create error: %v", err)
		}
		if gotArgs := readRecordedArgs(t); slices.Contains(gotArgs, "--json") {
			t.Errorf("argv contains --json; older gh rejects it on pr create: %q", gotArgs)
		}
	})

	t.Run("malformed url", func(t *testing.T) {
		t.Setenv("GH_STDOUT", "oops")
		in := PRInput{Base: "main", Head: "feature/x", Title: "t", Body: "b"}
		if _, err := (GitHubCLI{}).Create(context.Background(), "owner/repo", in); err == nil {
			t.Fatal("Create error = nil, want URL parse error")
		}
	})

	t.Run("empty output", func(t *testing.T) {
		t.Setenv("GH_STDOUT", "")
		in := PRInput{Base: "main", Head: "feature/x", Title: "t", Body: "b"}
		if _, err := (GitHubCLI{}).Create(context.Background(), "owner/repo", in); err == nil {
			t.Fatal("Create error = nil, want empty-output error")
		}
	})
}

// TestGitHubCLICreateBaseRefOID pins the AR-7 base-read: after creating a PR,
// Create resolves the PR's actual base commit from the REST pulls payload so
// delivery can verify the base still contains the admitted commit.
func TestGitHubCLICreateBaseRefOID(t *testing.T) {
	writeFakeGH(t)
	t.Setenv("GH_ARGS_FILE", filepath.Join(t.TempDir(), "args.txt"))
	t.Setenv("GH_API_ARGS_FILE", filepath.Join(t.TempDir(), "api-args.txt"))
	t.Setenv("GH_ENV_FILE", filepath.Join(t.TempDir(), "env.txt"))
	t.Setenv("GH_STDOUT", `https://github.com/owner/repo/pull/7`+"\n")

	t.Run("api resolves the base oid", func(t *testing.T) {
		t.Setenv("GH_STDOUT_API", `{"base":{"sha":"beef123"}}`)
		in := PRInput{Base: "main", Head: "feature/x", Title: "t", Body: "b"}
		got, err := (GitHubCLI{}).Create(context.Background(), "owner/repo", in)
		if err != nil {
			t.Fatalf("Create error: %v", err)
		}
		if got.BaseRefOID != "beef123" {
			t.Errorf("BaseRefOID = %q, want beef123 from gh api", got.BaseRefOID)
		}
		wantAPI := []string{"api", "repos/owner/repo/pulls/7"}
		if gotArgs := readRecordedAPIArgs(t); !slices.Equal(gotArgs, wantAPI) {
			t.Errorf("api argv = %q, want %q", gotArgs, wantAPI)
		}
	})

	t.Run("malformed api output", func(t *testing.T) {
		t.Setenv("GH_STDOUT_API", "not json")
		in := PRInput{Base: "main", Head: "feature/x", Title: "t", Body: "b"}
		if _, err := (GitHubCLI{}).Create(context.Background(), "owner/repo", in); err == nil {
			t.Fatal("Create error = nil, want a gh api parse error")
		}
	})

	t.Run("api payload without base.sha", func(t *testing.T) {
		t.Setenv("GH_STDOUT_API", `{"base":{}}`)
		in := PRInput{Base: "main", Head: "feature/x", Title: "t", Body: "b"}
		if _, err := (GitHubCLI{}).Create(context.Background(), "owner/repo", in); err == nil {
			t.Fatal("Create error = nil, want a missing base.sha error")
		}
	})
}

// TestGitHubCLIAPIExactOnePositionalEndpoint pins the DC-14 interface
// contract of the gh api boundary. Real gh declares
// `Args: cobra.ExactArgs(1)` on the api subcommand: exactly ONE positional
// endpoint. The old invocation passed a doubled "api" positional
// (`gh api api repos/<repo>/pulls/<n>`), which the fake gh accepted - a
// double more permissive than the real interface - so every delivery attempt
// died at the base-commit read after all gates had passed, with the run stuck
// delivery_pending forever. The faithful double now refuses the doubled shape
// exactly as real gh does, and the fixed code passes exactly one endpoint.
func TestGitHubCLIAPIExactOnePositionalEndpoint(t *testing.T) {
	writeFakeGH(t)
	t.Setenv("GH_ARGS_FILE", filepath.Join(t.TempDir(), "args.txt"))
	t.Setenv("GH_API_ARGS_FILE", filepath.Join(t.TempDir(), "api-args.txt"))
	t.Setenv("GH_ENV_FILE", filepath.Join(t.TempDir(), "env.txt"))

	// The double must refuse the buggy doubled-endpoint shape with real gh's
	// exact-args error, so a regression to the old invocation cannot pass
	// green behind a permissive fake (DC-14).
	t.Run("faithful double refuses a doubled endpoint", func(t *testing.T) {
		if _, err := runGH(context.Background(), "api", "api", "api", "repos/owner/repo/pulls/7"); err == nil {
			t.Fatal("runGH with a doubled api endpoint = nil error, want the real gh exact-args refusal")
		} else if !strings.Contains(err.Error(), "accepts 1 arg(s), received 2") {
			t.Fatalf("doubled-endpoint error = %v, want the cobra exact-args message", err)
		}
	})

	t.Run("FindByHead passes exactly one endpoint", func(t *testing.T) {
		t.Setenv("GH_STDOUT", `[{"number":12,"url":"https://github.com/o/r/pull/12","isDraft":true,"headRepositoryOwner":{"login":"owner"}}]`)
		t.Setenv("GH_STDOUT_API", `{"base":{"sha":"aaa111"}}`)
		got, err := (GitHubCLI{}).FindByHead(context.Background(), "owner/repo", "feature/x")
		if err != nil {
			t.Fatalf("FindByHead rejected by a faithful gh: %v", err)
		}
		if got == nil || got.BaseRefOID != "aaa111" {
			t.Fatalf("FindByHead = %+v, want the owner PR with BaseRefOID aaa111", got)
		}
		if gotArgs := readRecordedAPIArgs(t); !slices.Equal(gotArgs, []string{"api", "repos/owner/repo/pulls/12"}) {
			t.Errorf("api argv = %q, want exactly one endpoint %q", gotArgs, "repos/owner/repo/pulls/12")
		}
	})

	t.Run("Create passes exactly one endpoint", func(t *testing.T) {
		t.Setenv("GH_STDOUT", `https://github.com/owner/repo/pull/7`+"\n")
		t.Setenv("GH_STDOUT_API", `{"base":{"sha":"beef123"}}`)
		in := PRInput{Base: "main", Head: "feature/x", Title: "t", Body: "b"}
		got, err := (GitHubCLI{}).Create(context.Background(), "owner/repo", in)
		if err != nil {
			t.Fatalf("Create rejected by a faithful gh: %v", err)
		}
		if got.BaseRefOID != "beef123" {
			t.Fatalf("Create BaseRefOID = %q, want beef123", got.BaseRefOID)
		}
		if gotArgs := readRecordedAPIArgs(t); !slices.Equal(gotArgs, []string{"api", "repos/owner/repo/pulls/7"}) {
			t.Errorf("api argv = %q, want exactly one endpoint %q", gotArgs, "repos/owner/repo/pulls/7")
		}
	})
}

// TestGitHubCLINeverRequestsBaseRefOid is the regression guard for the
// delivery outage introduced by b977729: every gate passed and then delivery
// died on `gh pr list --json ...baseRefOid...` because released gh has no such
// field. No gh invocation may name it, on any path.
func TestGitHubCLINeverRequestsBaseRefOid(t *testing.T) {
	writeFakeGH(t)
	t.Setenv("GH_ARGS_FILE", filepath.Join(t.TempDir(), "args.txt"))
	t.Setenv("GH_API_ARGS_FILE", filepath.Join(t.TempDir(), "api-args.txt"))
	t.Setenv("GH_ENV_FILE", filepath.Join(t.TempDir(), "env.txt"))

	t.Run("FindByHead", func(t *testing.T) {
		t.Setenv("GH_STDOUT", `[{"number":12,"url":"https://github.com/o/r/pull/12","isDraft":true,"headRepositoryOwner":{"login":"owner"}}]`)
		if _, err := (GitHubCLI{}).FindByHead(context.Background(), "owner/repo", "feature/x"); err != nil {
			t.Fatalf("FindByHead rejected by a gh that lacks baseRefOid: %v", err)
		}
		for _, arg := range readRecordedArgs(t) {
			if strings.Contains(arg, "baseRefOid") {
				t.Errorf("pr list argv names baseRefOid: %q", arg)
			}
		}
	})

	t.Run("Create", func(t *testing.T) {
		t.Setenv("GH_STDOUT", `https://github.com/owner/repo/pull/7`+"\n")
		in := PRInput{Base: "main", Head: "feature/x", Title: "t", Body: "b"}
		if _, err := (GitHubCLI{}).Create(context.Background(), "owner/repo", in); err != nil {
			t.Fatalf("Create rejected by a gh that lacks baseRefOid: %v", err)
		}
		for _, arg := range append(readRecordedArgs(t), readRecordedAPIArgs(t)...) {
			if strings.Contains(arg, "baseRefOid") {
				t.Errorf("argv names baseRefOid: %q", arg)
			}
		}
	})
}
