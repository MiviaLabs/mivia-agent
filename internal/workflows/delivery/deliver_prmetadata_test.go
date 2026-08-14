package delivery

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// seedChangeSummary records a completed step attempt whose output JSON is the
// agent's change summary (pr_title/pr_summary), so the delivery engine's
// change-summary resolution can find it.
func seedChangeSummary(t *testing.T, repo workflowledger.Repository, run workflowledger.RunSnapshot, stepID string, attemptNo int, outputJSON string) {
	t.Helper()
	seedAttempt(t, repo, run.RunID, stepID, attemptNo, outputJSON, "")
}

// writeWorkspacePRTitlePolicy writes a pr-title.toml policy under the
// worktree's .mivia/policy directory and excludes .mivia/ from the fixture's
// index, so delivery reads the policy file but never commits it into the
// delivered diff.
func writeWorkspacePRTitlePolicy(t *testing.T, repoRoot, worktreeRoot, content string) {
	t.Helper()
	exclude := filepath.Join(repoRoot, ".git", "info", "exclude")
	f, err := os.OpenFile(exclude, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open git exclude: %v", err)
	}
	if _, err := f.WriteString("\n.mivia/\n"); err != nil {
		f.Close()
		t.Fatalf("append git exclude: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close git exclude: %v", err)
	}
	dir := filepath.Join(worktreeRoot, ".mivia", "policy")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pr-title.toml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write pr-title.toml: %v", err)
	}
}

// writeWorktreePRTitlePolicy writes a pr-title.toml policy at relPath under
// the worktree root — a workflow-declared pr_title_policy path — and excludes
// that path from the fixture's index, so delivery reads the policy file but
// never commits it into the delivered diff.
func writeWorktreePRTitlePolicy(t *testing.T, repoRoot, worktreeRoot, relPath, content string) {
	t.Helper()
	exclude := filepath.Join(repoRoot, ".git", "info", "exclude")
	f, err := os.OpenFile(exclude, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open git exclude: %v", err)
	}
	if _, err := f.WriteString("\n" + relPath + "\n"); err != nil {
		f.Close()
		t.Fatalf("append git exclude: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close git exclude: %v", err)
	}
	dir := filepath.Join(worktreeRoot, filepath.Dir(relPath))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(worktreeRoot, relPath), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", relPath, err)
	}
}

// failLoadContentRepo wraps a ledger.Repository and injects a failure into
// LoadContent for one content ref, so a test can make change-summary
// resolution fail at the storage boundary. Every other repository method
// delegates to the wrapped repository.
type failLoadContentRepo struct {
	workflowledger.Repository
	failRef string
	failErr error
}

func (f *failLoadContentRepo) LoadContent(ctx context.Context, ref string) ([]byte, error) {
	if ref == f.failRef {
		return nil, f.failErr
	}
	return f.Repository.LoadContent(ctx, ref)
}

// passingPRTitlePolicyTOML is a policy the agent title
// "feat(scope): add widget" satisfies.
const passingPRTitlePolicyTOML = `
[title]
pattern = '^[a-z]+\((?P<scope>[a-z]+)\): .+$'
scopes = ["scope"]
`

// scopeRestrictedPRTitlePolicyTOML is a policy whose allowed scope list the
// agent title "feat(scope): add widget" violates.
const scopeRestrictedPRTitlePolicyTOML = `
[title]
pattern = '^[a-z]+\((?P<scope>[a-z]+)\): .+$'
scopes = ["feat"]
`

// agentSummaryBody returns the exact PR body the delivery engine must build
// when the agent provides a change summary.
func agentSummaryBody(run workflowledger.RunSnapshot, summary string) string {
	return summary + "\n\n" + wantFooter(run)
}

// wantFooter returns the exact attribution + collapsible run-details block
// the delivery engine appends to every published PR body.
func wantFooter(run workflowledger.RunSnapshot) string {
	digestText := run.WorkflowDigest
	if len(digestText) > 12 {
		digestText = digestText[:12]
	}
	return "<details>\n<summary><sub>Mivia Agent run details</sub></summary>\n\n" +
		"- Run: [" + run.RunID + "](https://mivia.app/runs/" + run.RunID + ")\n" +
		"- Workflow digest: [" + digestText + "](https://mivia.app/workflows/digest/" + run.WorkflowDigest + ")\n" +
		"\n</details>\n\n---\n" +
		"<sub><img src=\"https://github.com/MiviaLabs.png\" width=\"16\" height=\"16\" align=\"top\" alt=\"Mivia Agent\" /> [Mivia Agent](https://github.com/MiviaLabs/mivia-agent)</sub>"
}

// TestDeliverAgentTitleAndSummary: a run whose attempts carry a change-summary
// output with pr_title/pr_summary publishes the agent title and a body that
// leads with the agent summary, even though a project pr-title policy exists.
func TestDeliverAgentTitleAndSummary(t *testing.T) {
	ctx := context.Background()
	repoRoot, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
	writeWorkspacePRTitlePolicy(t, repoRoot, worktreeRoot, passingPRTitlePolicyTOML)
	seedChangeSummary(t, repo, run, "implement", 2, `{"pr_title": "feat(scope): add widget", "pr_summary": "Adds the widget."}`)
	pr := &fakePRClient{}
	res, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "add delivery"}))
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if res.Status != "succeeded" {
		t.Fatalf("Result = %+v, want succeeded", res)
	}
	if n := pr.createdCount(); n != 1 {
		t.Fatalf("Create calls = %d, want 1", n)
	}
	c := pr.created[0]
	if c.Title != "feat(scope): add widget" {
		t.Fatalf("Title = %q, want the agent-provided title", c.Title)
	}
	want := agentSummaryBody(run, "Adds the widget.")
	if c.Body != want {
		t.Fatalf("Body = %q, want %q", c.Body, want)
	}
}

// TestDeliverAgentSummaryBodyExact pins the exact body string the engine
// builds when the agent provides a change summary.
func TestDeliverAgentSummaryBodyExact(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
	seedChangeSummary(t, repo, run, "implement", 1, `{"pr_title": "feat: x", "pr_summary": "Adds the widget."}`)
	pr := &fakePRClient{}
	if _, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"})); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	want := agentSummaryBody(run, "Adds the widget.")
	if got := pr.created[0].Body; got != want {
		t.Fatalf("Body = %q, want the exact spec body %q", got, want)
	}
}

// TestDeliverLegacyTitleFallback: without a change-summary output, the title
// comes from title_template and the body is the fixed legacy body.
func TestDeliverLegacyTitleFallback(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
	pr := &fakePRClient{}
	res, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "add delivery"}))
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if res.Status != "succeeded" {
		t.Fatalf("Result = %+v, want succeeded", res)
	}
	c := pr.created[0]
	if c.Title != "feat: add delivery" {
		t.Fatalf("Title = %q, want the legacy title_template render", c.Title)
	}
	if c.Body != wantBody(run) {
		t.Fatalf("Body = %q, want the fixed legacy body %q", c.Body, wantBody(run))
	}
}

// TestDeliverPRTitlePolicyViolation: a project pr-title policy that requires
// scope membership rejects an agent title outside the scope list with a
// PRMetadataError, BEFORE any push or PR client call.
func TestDeliverPRTitlePolicyViolation(t *testing.T) {
	ctx := context.Background()
	repoRoot, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
	writeWorkspacePRTitlePolicy(t, repoRoot, worktreeRoot, scopeRestrictedPRTitlePolicyTOML)
	seedChangeSummary(t, repo, run, "implement", 1, `{"pr_title": "feat(scope): add widget", "pr_summary": "Adds the widget."}`)
	pr := &fakePRClient{}
	_, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"}))
	if err == nil || !IsPRMetadataError(err) {
		t.Fatalf("Deliver err = %v, want PRMetadataError for the scope violation", err)
	}
	assertZeroCreates(t, pr)
	assertNoRecord(t, repo, run)
	assertNoBranchOnOrigin(t, repoRoot, originURL)
}

// TestDeliverAgentTitleTooLong: an agent title over GitHub's 256-rune ceiling
// is a PRMetadataError, not a truncation: the agent must fix the title.
func TestDeliverAgentTitleTooLong(t *testing.T) {
	ctx := context.Background()
	repoRoot, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
	long := strings.Repeat("x", MaxTitleRunes+1)
	seedChangeSummary(t, repo, run, "implement", 1, `{"pr_title": "`+long+`"}`)
	pr := &fakePRClient{}
	_, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"}))
	if err == nil || !IsPRMetadataError(err) {
		t.Fatalf("Deliver err = %v, want PRMetadataError for a %d-rune title", err, MaxTitleRunes+1)
	}
	assertZeroCreates(t, pr)
	assertNoRecord(t, repo, run)
	assertNoBranchOnOrigin(t, repoRoot, originURL)
}

// TestDeliverAgentTitleControlCharacter: an agent title that carries a control
// character is a PRMetadataError; the agent must fix the metadata.
func TestDeliverAgentTitleControlCharacter(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
	seedChangeSummary(t, repo, run, "implement", 1, "{\"pr_title\": \"feat: a\\nb\"}")
	pr := &fakePRClient{}
	_, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"}))
	if err == nil || !IsPRMetadataError(err) {
		t.Fatalf("Deliver err = %v, want PRMetadataError for a control character in the title", err)
	}
	assertZeroCreates(t, pr)
	assertNoRecord(t, repo, run)
}

// TestDeliverLegacyTitleValidatedByPolicy: the project pr-title policy
// constrains the LEGACY template title too, so a rendered title that violates
// the policy is a PRMetadataError before any push.
func TestDeliverLegacyTitleValidatedByPolicy(t *testing.T) {
	ctx := context.Background()
	repoRoot, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
	// The policy allows only the "feat" scope; the template renders
	// "feat: add delivery" with no scope, so the pattern's scope group is
	// empty and the membership check must refuse.
	writeWorkspacePRTitlePolicy(t, repoRoot, worktreeRoot, scopeRestrictedPRTitlePolicyTOML)
	pr := &fakePRClient{}
	_, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "add delivery"}))
	if err == nil || !IsPRMetadataError(err) {
		t.Fatalf("Deliver err = %v, want PRMetadataError for the legacy title scope violation", err)
	}
	assertZeroCreates(t, pr)
	assertNoRecord(t, repo, run)
	assertNoBranchOnOrigin(t, repoRoot, originURL)
}

// TestDeliverCustomPRTitlePolicyPath: a workflow that declares
// pr_title_policy = "policy/pr-title.toml" consults that EXACT file, not the
// default .mivia/policy/pr-title.toml (which is absent throughout): a custom
// policy that refuses the agent title stops delivery with a PRMetadataError
// before any push or create, and a custom policy that passes publishes the
// agent title.
func TestDeliverCustomPRTitlePolicyPath(t *testing.T) {
	ctx := context.Background()

	t.Run("refusing custom policy stops delivery", func(t *testing.T) {
		repoRoot, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
		writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
		writeWorktreePRTitlePolicy(t, repoRoot, worktreeRoot, "policy/pr-title.toml", scopeRestrictedPRTitlePolicyTOML)
		seedChangeSummary(t, repo, run, "implement", 1, `{"pr_title": "feat(scope): add widget", "pr_summary": "Adds the widget."}`)
		pol := defaultPolicy("draft")
		pol.PRTitlePolicyPath = "policy/pr-title.toml"
		pr := &fakePRClient{}
		_, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, pol, map[string]string{"task": "x"}))
		if err == nil || !IsPRMetadataError(err) {
			t.Fatalf("Deliver err = %v, want PRMetadataError from the custom policy", err)
		}
		assertZeroCreates(t, pr)
		assertNoRecord(t, repo, run)
		assertNoBranchOnOrigin(t, repoRoot, originURL)
	})

	t.Run("passing custom policy publishes the agent title", func(t *testing.T) {
		repoRoot, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
		writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
		writeWorktreePRTitlePolicy(t, repoRoot, worktreeRoot, "policy/pr-title.toml", passingPRTitlePolicyTOML)
		seedChangeSummary(t, repo, run, "implement", 1, `{"pr_title": "feat(scope): add widget", "pr_summary": "Adds the widget."}`)
		pol := defaultPolicy("draft")
		pol.PRTitlePolicyPath = "policy/pr-title.toml"
		pr := &fakePRClient{}
		res, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, pol, map[string]string{"task": "x"}))
		if err != nil {
			t.Fatalf("Deliver: %v", err)
		}
		if res.Status != "succeeded" {
			t.Fatalf("Result = %+v, want succeeded", res)
		}
		if n := pr.createdCount(); n != 1 {
			t.Fatalf("Create calls = %d, want 1", n)
		}
		if pr.created[0].Title != "feat(scope): add widget" {
			t.Fatalf("Title = %q, want the agent-provided title", pr.created[0].Title)
		}
	})
}

// TestDeliverRepairedTitleWinsOverImplementReentry pins the metadata-repair
// loop end to end at the delivery engine. The controller numbers attempts per
// step and restarts at 1 for the repair step (controller/linear_attempts.go),
// so in a real feature-delivery run a review-loop re-entry leaves implement
// with a HIGHER attempt number than the repair step that fixed the title
// later. The next delivery must validate the FIXED title (the last recorded
// change summary), never implement#2's stale one: a resolution that preferred
// the highest AttemptNo shadowed the repair's fix and every re-delivery
// re-failed byte-identically until the repair budget burned. Regression for
// the live loop; RED before the ResolveLatestChangeSummary recency fix.
func TestDeliverRepairedTitleWinsOverImplementReentry(t *testing.T) {
	ctx := context.Background()
	repoRoot, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
	// Mirrors .mivia/policy/pr-title.toml's pattern (scopes omitted so the
	// fixed title is not refused for an unrelated scope-list membership).
	writeWorkspacePRTitlePolicy(t, repoRoot, worktreeRoot, `[title]
pattern = '^(?P<type>feat|fix|docs|chore|refactor|test)(\((?P<scope>[a-z0-9-]+)\))?!?: .+$'
`)
	oldTitle := "textutil: add TruncateEllipsis rune-safe ellipsis truncation"
	fixedTitle := "feat(textutil): add TruncateEllipsis rune-safe ellipsis truncation"
	summary := "Adds the helper. Needed for delivery."
	// implement#1 and #2 (review-loop re-entry) both carry the old
	// non-conforming title; the repair step (attempt 1, recorded LAST) fixes
	// it — the exact ledger shape a real repair loop leaves behind.
	seedChangeSummary(t, repo, run, "implement", 1, `{"pr_title": "`+oldTitle+`", "pr_summary": "`+summary+`"}`)
	seedChangeSummary(t, repo, run, "implement", 2, `{"pr_title": "`+oldTitle+`", "pr_summary": "`+summary+`"}`)
	seedChangeSummary(t, repo, run, "repair_pr_metadata", 1, `{"pr_title": "`+fixedTitle+`", "pr_summary": "`+summary+`"}`)
	pr := &fakePRClient{}
	res, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "add delivery"}))
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if res.Status != "succeeded" {
		t.Fatalf("Result = %+v, want succeeded", res)
	}
	if n := pr.createdCount(); n != 1 {
		t.Fatalf("Create calls = %d, want 1", n)
	}
	if got := pr.created[0].Title; got != fixedTitle {
		t.Fatalf("Title = %q, want the repaired title %q", got, fixedTitle)
	}
}

// TestDeliverChangeSummaryLoadFailurePropagates: when the change-summary
// output cannot be loaded, validatePRMetadata must propagate the storage
// error instead of swallowing it and silently falling back to the legacy
// title template with a nil error.
func TestDeliverChangeSummaryLoadFailurePropagates(t *testing.T) {
	ctx := context.Background()
	repoRoot, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
	outputJSON := `{"pr_title": "feat: x", "pr_summary": "Adds the widget."}`
	seedChangeSummary(t, repo, run, "implement", 1, outputJSON)
	failing := &failLoadContentRepo{
		Repository: repo,
		failRef:    "sha256:" + workflowledger.DigestHex([]byte(outputJSON)),
		failErr:    errors.New("store: change summary content load failed"),
	}
	pr := &fakePRClient{}
	_, err := Deliver(ctx, failing, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"}))
	if err == nil {
		t.Fatal("Deliver err = nil, want the change-summary load failure to propagate")
	}
	if !strings.Contains(err.Error(), "store: change summary content load failed") {
		t.Fatalf("Deliver err = %v, want the storage error, not a silent legacy-title fallback", err)
	}
	assertZeroCreates(t, pr)
	assertNoRecord(t, repo, run)
	assertNoBranchOnOrigin(t, repoRoot, originURL)
}

// TestDeliverAgentSummaryControlCharacter: an agent summary that carries a
// control character is a PRMetadataError naming pr_summary, before any push
// or PR create — even when the pr-title policy itself would pass the summary.
// Mirrors TestDeliverAgentTitleControlCharacter.
func TestDeliverAgentSummaryControlCharacter(t *testing.T) {
	ctx := context.Background()
	repoRoot, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
	writeWorkspacePRTitlePolicy(t, repoRoot, worktreeRoot, passingPRTitlePolicyTOML)
	seedChangeSummary(t, repo, run, "implement", 1, "{\"pr_title\": \"feat(scope): add widget\", \"pr_summary\": \"Adds the widget.\\u0000\"}")
	pr := &fakePRClient{}
	_, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"}))
	if err == nil || !IsPRMetadataError(err) {
		t.Fatalf("Deliver err = %v, want PRMetadataError for a control character in the summary", err)
	}
	if !strings.Contains(err.Error(), "pr_summary") {
		t.Fatalf("Deliver err = %v, want a hint naming pr_summary", err)
	}
	assertZeroCreates(t, pr)
	assertNoRecord(t, repo, run)
	assertNoBranchOnOrigin(t, repoRoot, originURL)
}

// TestDeliverAgentSummaryMultiline: the PR body is a multi-line field, so a
// two-sentence summary with a line break survives sanitization and is
// published as-is; CRLF line endings are normalized to LF.
func TestDeliverAgentSummaryMultiline(t *testing.T) {
	ctx := context.Background()
	repoRoot, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
	writeWorkspacePRTitlePolicy(t, repoRoot, worktreeRoot, passingPRTitlePolicyTOML)
	seedChangeSummary(t, repo, run, "implement", 1, "{\"pr_title\": \"feat(scope): add widget\", \"pr_summary\": \"Adds the widget.\\r\\nNeeded for delivery.\"}")
	pr := &fakePRClient{}
	res, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"}))
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if res.Status != "succeeded" {
		t.Fatalf("Result = %+v, want succeeded", res)
	}
	if n := pr.createdCount(); n != 1 {
		t.Fatalf("Create calls = %d, want 1", n)
	}
	want := agentSummaryBody(run, "Adds the widget.\nNeeded for delivery.")
	if got := pr.created[0].Body; got != want {
		t.Fatalf("Body = %q, want %q", got, want)
	}
}

// TestDeliverBodyIncludesStackPartInRunDetails pins the collapsible run
// details section: a chunk admitted with a stack_part input must publish it
// inside <details>, alongside the run and workflow digest links.
func TestDeliverBodyIncludesStackPartInRunDetails(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
	seedChangeSummary(t, repo, run, "implement", 1, `{"pr_title": "feat: x", "pr_summary": "Adds the widget."}`)
	pr := &fakePRClient{}
	req := newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x", InputStackPart: "2/3"})
	if _, err := Deliver(ctx, repo, RealGit{}, pr, req); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	got := pr.created[0].Body
	if !strings.Contains(got, "- Stack part: 2/3\n") {
		t.Fatalf("Body = %q, want it to contain the stack part line inside run details", got)
	}
	if !strings.HasSuffix(got, "---\n<sub><img src=\"https://github.com/MiviaLabs.png\" width=\"16\" height=\"16\" align=\"top\" alt=\"Mivia Agent\" /> [Mivia Agent](https://github.com/MiviaLabs/mivia-agent)</sub>") {
		t.Fatalf("Body = %q, want the avatar attribution line LAST, after a horizontal rule", got)
	}
	if !strings.Contains(got, "<details>\n<summary><sub>Mivia Agent run details</sub></summary>") {
		t.Fatalf("Body = %q, want the collapsible run-details section", got)
	}
}

// TestDeliverBodyOmitsStackPartWhenAbsent: a non-stacked delivery (no
// stack_part input) publishes the run-details section without a Stack part
// line - the field must not appear as an empty or zero-value line.
func TestDeliverBodyOmitsStackPartWhenAbsent(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
	seedChangeSummary(t, repo, run, "implement", 1, `{"pr_title": "feat: x", "pr_summary": "Adds the widget."}`)
	pr := &fakePRClient{}
	if _, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"})); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if got := pr.created[0].Body; strings.Contains(got, "Stack part") {
		t.Fatalf("Body = %q, want no Stack part line for a non-stacked delivery", got)
	}
}
