package delivery

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// TestFollowUpPRContentLinksTheParentPR pins the core requirement: every PR
// a deferred follow-up's title/body mentions is a real, clickable link
// ("[#142](url)"), never a bare "#142" a reviewer has to resolve by hand,
// and the title reuses the parent's own agent-authored title rather than
// inventing a disconnected sentence from a raw run ID.
func TestFollowUpPRContentLinksTheParentPR(t *testing.T) {
	parentRef := &PRRef{RemoteID: "142", URL: "https://github.com/MiviaLabs/mivia-agent/pull/142", Title: "fix(agent): retain interrupted turns"}
	title, body := followUpPRContent("wfr-inv-abc123", "wf/wt-test", parentRef, []string{"internal/foo/bar.go", "internal/foo/bar_test.go"})

	wantTitle := "fix(agent): retain interrupted turns [split 2/2, base: #142]"
	if title != wantTitle {
		t.Fatalf("title = %q, want %q", title, wantTitle)
	}
	link := "[#142](https://github.com/MiviaLabs/mivia-agent/pull/142)"
	if n := strings.Count(body, link); n < 2 {
		t.Fatalf("body must link #142 (full URL) everywhere it is mentioned, want >=2 occurrences of %q, got %d in body:\n%s", link, n, body)
	}
	if strings.Contains(body, "#142") && strings.Count(body, "#142") != strings.Count(body, link) {
		t.Fatalf("body mentions #142 outside a markdown link; every mention must be linked. body:\n%s", body)
	}
	for _, f := range []string{"internal/foo/bar.go", "internal/foo/bar_test.go"} {
		if !strings.Contains(body, f) {
			t.Fatalf("body missing deferred file %q; want the real file list, not a generic sentence. body:\n%s", f, body)
		}
	}
	if strings.Contains(body, "wfr-inv-abc123") {
		t.Fatalf("body must not fall back to the raw run ID when the parent PR was found; body:\n%s", body)
	}
}

// TestFollowUpPRContentSanitizesReusedTitle pins a real gap an adversarial
// review pass caught: parentRef.Title comes LIVE from GitHub (pr.FindByHead),
// not from our own sanitizeAgentTitle - a human can hand-edit a PR's title
// on GitHub after creation, bypassing every host-side check that ran once at
// creation time. followUpPRContent must never trust that text blindly: a
// control character or embedded newline must never reach pr.Create's
// --title= argv.
func TestFollowUpPRContentSanitizesReusedTitle(t *testing.T) {
	parentRef := &PRRef{
		RemoteID: "142",
		URL:      "https://github.com/MiviaLabs/mivia-agent/pull/142",
		Title:    "fix(agent): retain\x00interrupted\x1b[31mturns\nwith a second line",
	}
	title, _ := followUpPRContent("wfr-inv-abc123", "wf/wt-test", parentRef, nil)
	if strings.ContainsAny(title, "\x00\x1b") {
		t.Fatalf("title must never carry a control character from a hand-edited parent title, got %q", title)
	}
	if strings.ContainsAny(title, "\n\r") {
		t.Fatalf("title must be single-line (a PR title is a one-line field), got %q", title)
	}
	// The ESC byte (a control char) is stripped, but the printable
	// characters it happened to precede ("[31m", an ANSI SGR sequence
	// missing its ESC) are ordinary text, not control characters - they are
	// NOT stripped. Sanitization removes control bytes, not arbitrary
	// "looks like an escape code" substrings.
	wantPrefix := "fix(agent): retaininterrupted[31mturns with a second line"
	if !strings.HasPrefix(title, wantPrefix) {
		t.Fatalf("title = %q, want it to start with the sanitized parent title %q", title, wantPrefix)
	}
}

// TestFollowUpPRContentTitleNeverExceedsGitHubLimit pins a real bug a review
// pass caught: a reused parent title can already be at MaxTitleRunes (GitHub
// itself truncates a created title to that limit - prmetadata_validate.go),
// so appending the "[split 2/2, base: ...]" affix unguarded reliably
// overflows GitHub's 256-rune ceiling. EnsureFollowUpPublished has no repair
// loop: an over-length title here would fail pr.Create outright and leave the
// deferred branch permanently unpublished. The title must always fit, even
// when that means truncating the reused parent title - the same
// truncate-don't-reject response appendStackPartTitle uses.
func TestFollowUpPRContentTitleNeverExceedsGitHubLimit(t *testing.T) {
	longTitle := strings.Repeat("a", MaxTitleRunes) // already at the ceiling
	parentRef := &PRRef{RemoteID: "142", URL: "https://github.com/MiviaLabs/mivia-agent/pull/142", Title: longTitle}
	title, _ := followUpPRContent("wfr-inv-abc123", "wf/wt-test", parentRef, nil)
	if n := utf8.RuneCountInString(title); n > MaxTitleRunes {
		t.Fatalf("title is %d runes, want <= %d (GitHub's limit); title:\n%s", n, MaxTitleRunes, title)
	}
	if !strings.HasSuffix(title, "[split 2/2, base: #142]") {
		t.Fatalf("title must still end with the full affix even when the base is truncated, got %q", title)
	}
}

// TestFollowUpPRContentMergeOrderIsThisPRFirst pins the merge-order
// direction: this PR is created with Base: parentBranch, Head:
// deferredBranch (followup.go's pr.Create call), so it merges INTO the
// parent branch - meaning THIS PR merges first, and the parent PR carries
// the deferred commit along when IT merges second. Stating the reverse
// would tell a reviewer to merge the parent first, which either fails (this
// PR's base branch would already be gone) or silently drops the deferred
// scope from main.
func TestFollowUpPRContentMergeOrderIsThisPRFirst(t *testing.T) {
	parentRef := &PRRef{RemoteID: "142", URL: "https://github.com/MiviaLabs/mivia-agent/pull/142", Title: "fix(agent): retain interrupted turns"}
	_, body := followUpPRContent("wfr-inv-abc123", "wf/wt-test", parentRef, []string{"a.go"})
	link := "[#142](https://github.com/MiviaLabs/mivia-agent/pull/142)"
	want := "Merge order: this PR -> " + link + "."
	if !strings.Contains(body, want) {
		t.Fatalf("body missing %q (this PR must merge BEFORE its base #142); body:\n%s", want, body)
	}
	wrong := "Merge order: " + link + " -> this PR."
	if strings.Contains(body, wrong) {
		t.Fatalf("body states the merge order backwards (%q); this PR's base is the parent branch, so it merges first, not last", wrong)
	}
}

// TestFollowUpPRContentFallsBackWithoutParentRef pins the degraded path: a
// failed parent lookup must never block publication, and must never fail
// silently either - the label/branch name is a legible fallback identifier.
func TestFollowUpPRContentFallsBackWithoutParentRef(t *testing.T) {
	title, body := followUpPRContent("wfr-inv-abc123", "wf/wt-test", nil, nil)
	if !strings.Contains(title, "wfr-inv-abc123") {
		t.Fatalf("title without a parent ref must fall back to the label, got %q", title)
	}
	if !strings.Contains(title, "[split 2/2, base: wf/wt-test]") {
		t.Fatalf("title without a parent ref must fall back to the branch name for the base tag, got %q", title)
	}
	if !strings.Contains(body, "wf/wt-test") {
		t.Fatalf("body without a parent ref must still name the base branch, got %q", body)
	}
}

// TestEnsureFollowUpPublishedLinksParentPR is the real end-to-end wiring
// test: a real git worktree (RealGit, not a fake), a real ledger record with
// DeferredFiles, and a branch-aware fake PRClient that distinguishes the
// parent branch lookup from the deferred branch lookup - proving
// EnsureFollowUpPublished actually calls FindByHead on the PARENT branch
// (not just the deferred one) and threads its result into the created PR's
// title/body.
// seedFollowUpFixture wires a real deferred branch plus the ledger record
// EnsureFollowUpPublished reads, and returns the parent/deferred branch
// names and the resolved repo slug.
func seedFollowUpFixture(t *testing.T, worktreeRoot string, ledgerRepo workflowledger.Repository, run workflowledger.RunSnapshot) (parentBranch, deferredBranch, slug string) {
	t.Helper()
	ctx := context.Background()
	parentBranch = "wf/wt-test"
	deferredBranch = DeferredBranchName(parentBranch)

	// Seed the deferred branch locally, as freshDeliveryCommitSplit leaves it.
	writeWorktreeFile(t, worktreeRoot, "deferred_a.go", "package x\n")
	runGit(t, worktreeRoot, "add", "-A")
	runGit(t, worktreeRoot, "-c", "user.name=Mivia Agent", "-c", "user.email=noreply@mivia.app",
		"commit", "--allow-empty-message", "-m", "deferred commit")
	runGit(t, worktreeRoot, "branch", "-f", deferredBranch, "HEAD")

	// Seed the ledger record HasDeferredFollowUp/latestDeferredDeliveryRecord
	// reads: succeeded, StackRemainingCommits > 0, real DeferredFiles.
	if err := ledgerRepo.UpsertDelivery(ctx, workflowledger.DeliveryRecord{
		RunID: run.RunID, IdempotencyKey: DeliveryKey(run.RunID, run.WorkflowDigest),
		Mode: "draft", BaseRef: "main", HeadRef: parentBranch,
		Provider: "github", Status: "succeeded",
		StackRemainingCommits: 1,
		DeferredFiles:         `["internal/foo/bar.go","internal/foo/bar_test.go"]`,
	}); err != nil {
		t.Fatal(err)
	}
	slug, err := ParseOwnerRepo(run.RemoteURL)
	if err != nil {
		t.Fatalf("ParseOwnerRepo: %v", err)
	}
	return parentBranch, deferredBranch, slug
}

func TestEnsureFollowUpPublishedLinksParentPR(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, _, _, _, run, ledgerRepo := newDeliveryFixture(t)
	run.WorktreeName = "wt-test" // matches the "wf/wt-test" branch newDeliveryFixture creates
	// ParseOwnerRepo (unlike deliver.go's own eligibility path) requires a
	// github.com remote; the fixture's local bare origin.git is only the git
	// push target, not the metadata slug, so override RemoteURL the same way
	// production separates "where git pushes" from "which repo the PR API
	// addresses."
	run.RemoteURL = "https://github.com/MiviaLabs/mivia-agent.git"

	parentBranch, deferredBranch, slug := seedFollowUpFixture(t, worktreeRoot, ledgerRepo, run)
	pr := &branchAwarePRClient{
		byHead: map[string]*PRRef{
			parentBranch: {RemoteID: "142", URL: "https://github.com/MiviaLabs/mivia-agent/pull/142", Title: "fix(agent): retain interrupted turns"},
			// deferredBranch intentionally absent: forces Create.
		},
	}

	branch, sha, ref, published, err := EnsureFollowUpPublished(ctx, RealGit{}, pr, worktreeRoot, ledgerRepo, run, run.RunID, nil)
	if err != nil {
		t.Fatalf("EnsureFollowUpPublished: %v", err)
	}
	if !published {
		t.Fatal("published = false, want true")
	}
	if branch != deferredBranch {
		t.Fatalf("branch = %q, want %q", branch, deferredBranch)
	}
	if sha == "" {
		t.Fatal("sha is empty")
	}
	if ref.RemoteID == "" {
		t.Fatal("created PR has no RemoteID")
	}
	if len(pr.created) != 1 {
		t.Fatalf("Create calls = %d, want 1", len(pr.created))
	}
	created := pr.created[0]
	wantTitle := "fix(agent): retain interrupted turns [split 2/2, base: #142]"
	if created.Title != wantTitle {
		t.Fatalf("created title = %q, want %q", created.Title, wantTitle)
	}
	link := "[#142](https://github.com/MiviaLabs/mivia-agent/pull/142)"
	if !strings.Contains(created.Body, link) {
		t.Fatalf("created body must link the parent PR (#142) with its full URL, got:\n%s", created.Body)
	}
	if !strings.Contains(created.Body, "internal/foo/bar.go") || !strings.Contains(created.Body, "internal/foo/bar_test.go") {
		t.Fatalf("created body must list the real deferred files from the ledger record, got:\n%s", created.Body)
	}
	if pr.repos[0] != slug {
		t.Fatalf("Create repo slug = %q, want %q", pr.repos[0], slug)
	}
}

// branchAwarePRClient is a PRClient whose FindByHead result depends on the
// queried branch, unlike fakePRClient (which always returns the same
// f.found regardless of branch) - EnsureFollowUpPublished now queries TWO
// different branches (parent and deferred) and must get two different
// answers.
type branchAwarePRClient struct {
	byHead  map[string]*PRRef
	repos   []string
	created []PRInput
}

func (f *branchAwarePRClient) FindByHead(_ context.Context, repo, headBranch string) (*PRRef, error) {
	f.repos = append(f.repos, repo)
	return f.byHead[headBranch], nil
}

func (f *branchAwarePRClient) IsMerged(context.Context, string, string) (bool, error) {
	return false, nil
}

func (f *branchAwarePRClient) Create(_ context.Context, repo string, in PRInput) (PRRef, error) {
	f.repos = append(f.repos, repo)
	f.created = append(f.created, in)
	n := len(f.created)
	return PRRef{RemoteID: "9" + string(rune('0'+n)), URL: "https://example.com/pull/9" + string(rune('0'+n))}, nil
}
