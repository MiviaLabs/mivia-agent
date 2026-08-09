// Package delivery implements host-owned pull-request publication for
// workflow runs. The GitHub CLI adapter uses fixed argv only. It never
// passes values through a shell.
package delivery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// PRRef identifies one remote pull request.
type PRRef struct {
	RemoteID   string
	URL        string
	Draft      bool
	BaseRefOID string // the PR's current base commit (gh baseRefOid)
}

// PRInput is the fixed set of values for PR creation. Values come from
// host-rendered templates; they are passed as single argv elements, never
// through a shell.
type PRInput struct {
	Base  string
	Head  string
	Title string
	Body  string
	Draft bool
}

// PRClient is the remote PR boundary. Implementations are host-owned.
type PRClient interface {
	FindByHead(ctx context.Context, repo, headBranch string) (*PRRef, error)
	Create(ctx context.Context, repo string, in PRInput) (PRRef, error)
}

// GitHubCLI drives the operator's gh binary with fixed argv and --repo.
type GitHubCLI struct{}

// Compile-time check that GitHubCLI satisfies PRClient.
var _ PRClient = GitHubCLI{}

// ghEnv returns the environment for a gh subprocess: the process
// environment minus the git-affecting variables that gitops.go pins for
// git commands, plus interactive prompts disabled. gh shells out to git
// for repository access, so a leaked repository pointer or credential
// variable would change which repository git operates on and how it
// authenticates.
func ghEnv() []string {
	env := make([]string, 0, len(os.Environ())+1)
	for _, kv := range os.Environ() {
		name, _, found := strings.Cut(kv, "=")
		if !found {
			continue
		}
		if _, removed := gitEnvRemoved[name]; removed {
			continue
		}
		if name == "GH_PROMPT_DISABLED" {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "GH_PROMPT_DISABLED=1")
}

// FindByHead lists open PRs whose head branch matches and returns the
// first PR whose head repository belongs to the target repository's owner. A
// fork PR with the same branch name must never be reused as this delivery's
// PR. It returns (nil, nil) when no matching open PR exists.
func (GitHubCLI) FindByHead(ctx context.Context, repo, headBranch string) (*PRRef, error) {
	args := []string{
		"pr", "list",
		"--repo", repo,
		"--head", headBranch,
		"--state", "open",
		"--json", "number,url,isDraft,headRepositoryOwner",
	}
	out, err := runGH(ctx, "pr list", args...)
	if err != nil {
		return nil, err
	}
	var prs []struct {
		Number        int    `json:"number"`
		URL           string `json:"url"`
		Draft         bool   `json:"isDraft"`
		HeadRepoOwner struct {
			Login string `json:"login"`
		} `json:"headRepositoryOwner"`
	}
	if err := json.Unmarshal(out, &prs); err != nil {
		return nil, fmt.Errorf("gh pr list: parse output: %w", err)
	}
	owner, _, ok := strings.Cut(repo, "/")
	if !ok {
		return nil, fmt.Errorf("gh pr list: repo %q is not owner/repo", repo)
	}
	for _, pr := range prs {
		// GitHub owner names are case-insensitive; the configured owner
		// keeps the casing from the remote URL and may not match the API.
		if !strings.EqualFold(pr.HeadRepoOwner.Login, owner) {
			continue
		}
		number := strconv.Itoa(pr.Number)
		baseOID, err := baseRefOID(ctx, repo, number)
		if err != nil {
			return nil, err
		}
		return &PRRef{RemoteID: number, URL: pr.URL, Draft: pr.Draft, BaseRefOID: baseOID}, nil
	}
	return nil, nil
}

// Create opens a pull request with the fixed input values. Title and
// body use the --title= and --body= equals forms, so values that start
// with '-' stay safe as single argv elements. The PR URL is parsed from
// stdout (gh pr create prints it on success); --json is not used because
// older gh versions do not support it on pr create. The created PR's base
// commit is read back from the REST API so the caller can verify the base
// still contains the admitted commit (delivery recovery, AR-7).
func (GitHubCLI) Create(ctx context.Context, repo string, in PRInput) (PRRef, error) {
	args := []string{
		"pr", "create",
		"--repo", repo,
		"--base", in.Base,
		"--head", in.Head,
		"--title=" + in.Title,
		"--body=" + in.Body,
	}
	if in.Draft {
		args = append(args, "--draft")
	}
	out, err := runGH(ctx, "pr create", args...)
	if err != nil {
		return PRRef{}, err
	}
	url := strings.TrimSpace(string(out))
	if url == "" {
		return PRRef{}, fmt.Errorf("gh pr create: no URL in output")
	}
	number, err := prNumberFromURL(url)
	if err != nil {
		return PRRef{}, fmt.Errorf("gh pr create: %w", err)
	}
	ref := PRRef{RemoteID: number, URL: url}
	ref.BaseRefOID, err = baseRefOID(ctx, repo, number)
	if err != nil {
		return PRRef{}, err
	}
	return ref, nil
}

// baseRefOID resolves a PR's current base commit via the REST API.
//
// It deliberately does NOT use `gh pr view --json baseRefOid`: that field is
// absent from gh's pr list/view field sets on released versions still in wide
// use (gh 2.46 rejects it with "Unknown JSON field"), which fails delivery
// after every gate has already passed. The REST payload's base.sha carries the
// same value and is not gated on the local gh build.
//
// The gh api invocation carries EXACTLY ONE positional endpoint: real gh
// declares `Args: cobra.ExactArgs(1)` on the api subcommand, so a doubled
// endpoint argument is refused with "accepts 1 arg(s), received 2" (DC-14).
// runGH's op label is used only in error messages; the argv itself must
// carry the "api" subcommand as its first gh argument.
func baseRefOID(ctx context.Context, repo, number string) (string, error) {
	out, err := runGH(ctx, "api", "api", "repos/"+repo+"/pulls/"+number)
	if err != nil {
		return "", err
	}
	var parsed struct {
		Base struct {
			SHA string `json:"sha"`
		} `json:"base"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return "", fmt.Errorf("gh api pulls: parse output: %w", err)
	}
	if parsed.Base.SHA == "" {
		return "", fmt.Errorf("gh api pulls: response has no base.sha for PR %s", number)
	}
	return parsed.Base.SHA, nil
}

// prNumberFromURL extracts the numeric PR identifier from a GitHub pull
// request URL of the form .../pull/<number>.
func prNumberFromURL(url string) (string, error) {
	idx := strings.LastIndex(url, "/pull/")
	if idx < 0 {
		return "", fmt.Errorf("parse PR number: URL %q has no /pull/ segment", url)
	}
	number := strings.TrimSpace(url[idx+len("/pull/"):])
	if _, err := strconv.Atoi(number); err != nil {
		return "", fmt.Errorf("parse PR number: %q is not numeric", number)
	}
	return number, nil
}

// runGH runs gh with fixed argv and the pinned environment. A non-zero
// exit is an error that includes the stderr output; it is never treated
// as an empty result.
func runGH(ctx context.Context, op string, args ...string) ([]byte, error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Env = ghEnv()
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("gh %s: %w: %s", op, err, msg)
		}
		return nil, fmt.Errorf("gh %s: %w", op, err)
	}
	return out, nil
}

// ParseOwnerRepo normalizes a git remote URL to owner/repo. It supports
// the https, scp-like git@, and ssh:// forms. The host must be
// github.com (case-insensitive) and the path must be exactly owner/repo
// with an optional trailing .git.
func ParseOwnerRepo(url string) (string, error) {
	raw := strings.TrimSpace(url)
	if raw == "" {
		return "", fmt.Errorf("parse owner/repo: empty remote URL")
	}
	host, path, err := splitRemote(raw)
	if err != nil {
		return "", fmt.Errorf("parse owner/repo: %w", err)
	}
	if !strings.EqualFold(host, "github.com") {
		return "", fmt.Errorf("parse owner/repo: host %q is not github.com", host)
	}
	path = strings.Trim(path, "/")
	path = strings.TrimSuffix(path, ".git")
	parts := strings.Split(path, "/")
	if len(parts) != 2 {
		return "", fmt.Errorf("parse owner/repo: path must be exactly owner/repo, got %q", path)
	}
	owner, repo := parts[0], parts[1]
	if owner == "" || repo == "" {
		return "", fmt.Errorf("parse owner/repo: owner and repo must not be empty")
	}
	return owner + "/" + repo, nil
}

// splitRemote splits a git remote URL into host and path.
func splitRemote(raw string) (string, string, error) {
	if !strings.Contains(raw, "://") {
		// scp-like syntax: [user@]host:path
		userHost, path, ok := strings.Cut(raw, ":")
		if !ok {
			return "", "", fmt.Errorf("remote %q has no host separator", raw)
		}
		if path == "" {
			return "", "", fmt.Errorf("remote %q has an empty path", raw)
		}
		host := userHost
		if i := strings.LastIndexByte(host, '@'); i >= 0 {
			host = host[i+1:]
		}
		if host == "" {
			return "", "", fmt.Errorf("remote %q has an empty host", raw)
		}
		return host, path, nil
	}
	rest := raw[strings.Index(raw, "://")+3:]
	if at := strings.IndexByte(rest, '@'); at >= 0 {
		if slash := strings.IndexByte(rest, '/'); slash < 0 || at < slash {
			rest = rest[at+1:]
		}
	}
	host, path, ok := strings.Cut(rest, "/")
	if !ok {
		return "", "", fmt.Errorf("remote %q has no path", raw)
	}
	if host == "" {
		return "", "", fmt.Errorf("remote %q has an empty host", raw)
	}
	return host, "/" + path, nil
}
