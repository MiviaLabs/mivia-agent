package delivery

import (
	"context"
	"errors"
	"strings"
	"testing"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// TestDeferredSplitSeparatesCompanion pins the split guard's decision table:
// deferring a file while keeping its test companion (or vice versa) must be
// refused - the delivered commit would fail the repository's own test gate
// for reasons the evidence gates never saw (the observed delivery-repair
// death loop). Language-agnostic: *_test, *.test, *_spec, *.spec, test_*,
// Test* in the same directory.
func TestDeferredSplitSeparatesCompanion(t *testing.T) {
	cases := []struct {
		name             string
		deferred, kept   []string
		wantDeferredPath string // "" means no separation
	}{
		{"source kept, test deferred", []string{"app_test.go"}, []string{"app.go"}, "app_test.go"},
		{"test kept, source deferred", []string{"app.go"}, []string{"app_test.go"}, "app.go"},
		{"unrelated files", []string{"the.big.txt"}, []string{"essential.txt"}, ""},
		{"only code file", []string{"app.go"}, []string{"other.go"}, ""},
		{"nested dir same-directory pair", []string{"pkg/helper_test.go"}, []string{"pkg/helper.go"}, "pkg/helper_test.go"},
		{"other-dir test not a companion", []string{"other/app_test.go"}, []string{"pkg/app.go"}, ""},
		{"spec style", []string{"api.spec.ts"}, []string{"api.ts"}, "api.spec.ts"},
		{"test_ prefix style", []string{"test_fts.py"}, []string{"fts.py"}, "test_fts.py"},
		{"Test prefix style", []string{"TestWidget.java"}, []string{"Widget.java"}, "TestWidget.java"},
		{"both deferred together is fine", []string{"app.go", "app_test.go"}, []string{"other.go"}, ""},
		{"neither deferred is fine", []string{"the.big.txt"}, []string{"app.go", "app_test.go"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dPath, kPath := deferredSplitSeparatesCompanion(tc.deferred, tc.kept)
			if tc.wantDeferredPath == "" {
				if kPath != "" {
					t.Fatalf("split refused: deferred %s separated from kept %s, want no refusal", dPath, kPath)
				}
				return
			}
			if dPath != tc.wantDeferredPath || kPath == "" {
				t.Fatalf("split refusal = (%q, %q), want deferred path %q with a kept companion", dPath, kPath, tc.wantDeferredPath)
			}
		})
	}
}

// TestDeliverAutoSplitRefusesTestCompanionSplit pins the guard end-to-end: a
// diff where the largest file is a test file must NOT be auto-split by
// deferring that test file while keeping its source in the delivered commit.
// The host refuses with a repairable DiffSizeError instead of committing a
// delivered commit that fails the repository's own test gate.
func TestDeliverAutoSplitRefusesTestCompanionSplit(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	// app.go is small; app_test.go is large. Deferring the largest file
	// (app_test.go) would keep app.go delivered without its tests.
	writeWorktreeFile(t, worktreeRoot, "app.go", "package app\n")
	writeWorktreeFile(t, worktreeRoot, "app_test.go", strings.Repeat("line\n", 50))

	policy := defaultPolicy("draft")
	policy.StackingHardLines = 5
	policy.SplitDeferred = true

	pr := &fakePRClient{}
	_, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, policy, map[string]string{"task": "gate"}))
	if err == nil {
		t.Fatal("Deliver succeeded, want the auto split to be refused (deferring app_test.go would separate it from app.go)")
	}
	if !IsDiffSizeError(err) {
		t.Fatalf("err = %v, want a DiffSizeError", err)
	}
	if !strings.Contains(err.Error(), "test companion") {
		t.Fatalf("err = %q, want it to name the test-companion separation", err.Error())
	}
	// Nothing was committed or split: the refusal happens before any commit.
	if delivered := runGitOut(t, worktreeRoot, "diff", "--name-only", baseCommit, "HEAD"); delivered != "" {
		t.Fatalf("delivered commit files = %q, want none (the refused split must not commit anything)", delivered)
	}
}

// TestFreshDeliveryCommitSplitRefusesTestCompanionSplit pins the repair-side
// guard: a repair agent's deferred_files that separates a file from its test
// companion must be refused with a repairable DiffSizeError BEFORE any commit
// - otherwise the delivered commit fails the repository's own test gate.
func TestFreshDeliveryCommitSplitRefusesTestCompanionSplit(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "app.go", "package app\n")
	writeWorktreeFile(t, worktreeRoot, "app_test.go", "package app\n")
	req := newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"})

	// The agent declared deferred_files = ["app_test.go"]: the test ships to
	// the follow-up PR while the code stays in the delivered commit.
	_, _, err := freshDeliveryCommitSplit(ctx, repo, RealGit{}, req, "key", "diff", []string{"app_test.go"}, "title", "body")
	if err == nil {
		t.Fatal("freshDeliveryCommitSplit succeeded, want refusal (deferring app_test.go without app.go)")
	}
	if !IsDiffSizeError(err) {
		t.Fatalf("err = %v, want a DiffSizeError", err)
	}
	if !strings.Contains(err.Error(), "test companion") {
		t.Fatalf("err = %q, want it to name the test-companion separation", err.Error())
	}
	if delivered := runGitOut(t, worktreeRoot, "diff", "--name-only", baseCommit, "HEAD"); delivered != "" {
		t.Fatalf("delivered commit files = %q, want none (the refused split must not commit anything)", delivered)
	}
}

// hintGit fakes GitRunner for the inventory hint, returning canned output per
// command. failAll simulates a broken worktree where the hint degrades to "".
type hintGit struct {
	outputs map[string]string
	failAll bool
	calls   []string
}

func (g *hintGit) Run(ctx context.Context, gc GitContext, args ...string) (string, error) {
	key := strings.Join(args, " ")
	g.calls = append(g.calls, key)
	if g.failAll {
		return "", errors.New("boom")
	}
	return g.outputs[key], nil
}

// TestDeliveryInventoryHintSections pins the failure diagnostic a repair
// agent sees after a pre-push hook rejection: which files the delivered
// commit carries, which were deferred, and which worktree changes the
// delivered commit does not carry - plus the guidance that the hook verified
// the DELIVERED COMMIT tree, not the worktree. The sections must appear in
// that order and the hint must degrade to "" when git is unusable.
func TestDeliveryInventoryHintSections(t *testing.T) {
	ctx := context.Background()
	git := &hintGit{outputs: map[string]string{
		"diff --name-only abc123..HEAD":        "app.go\napp_test.go\n",
		"diff --name-only HEAD":                "app.go\n",
		"ls-files --others --exclude-standard": "new.txt\n",
	}}
	req := Request{BaseCommit: "abc123", Branch: "wf/wt-test", Inputs: map[string]string{InputDeferredFiles: `["app_test.go"]`}}
	existing := workflowledger.DeliveryRecord{}

	hint := deliveryInventoryHint(ctx, git, req, existing)
	if hint == "" {
		t.Fatal("hint = \"\", want the inventory sections")
	}
	for _, want := range []string{
		"delivered commit files (base..HEAD):\n  app.go\n  app_test.go",
		"deferred files (excluded from the delivered commit; follow-up PR):\n  app_test.go",
		"worktree changes not in the delivered commit:\n  app.go\n  new.txt",
		"verified the DELIVERED COMMIT tree",
		"Do NOT revert production code",
	} {
		if !strings.Contains(hint, want) {
			t.Fatalf("hint missing %q:\n%s", want, hint)
		}
	}
	committed := strings.Index(hint, "delivered commit files")
	deferred := strings.Index(hint, "deferred files")
	pending := strings.Index(hint, "worktree changes")
	if !(committed < deferred && deferred < pending) {
		t.Fatalf("hint sections out of order (committed=%d deferred=%d worktree=%d):\n%s", committed, deferred, pending, hint)
	}

	// Deferred decision from a previous attempt's record is honored too (the
	// resume path carries it on the record, not on the run's inputs).
	reqNoInputs := Request{BaseCommit: "abc123", Branch: "wf/wt-test"}
	existingWithSplit := workflowledger.DeliveryRecord{DeferredFiles: `["app_test.go"]`}
	if hint2 := deliveryInventoryHint(ctx, git, reqNoInputs, existingWithSplit); !strings.Contains(hint2, "deferred files (excluded from the delivered commit; follow-up PR):\n  app_test.go") {
		t.Fatalf("hint from recorded DeferredFiles missing the deferred section:\n%s", hint2)
	}

	// Degradation: a broken worktree must never mask the caller's original
	// error, and with no git AND no recorded split decision the hint is
	// genuinely empty (best-effort diagnostic). The deferred section is
	// git-independent (it comes from the recorded decision), so it must still
	// render when git is unusable - it is the most important clue for the
	// delivered-vs-worktree divergence.
	broken := &hintGit{failAll: true}
	if hint3 := deliveryInventoryHint(ctx, broken, req, existing); !strings.Contains(hint3, "deferred files (excluded from the delivered commit; follow-up PR):\n  app_test.go") {
		t.Fatalf("hint with a failing git = %q, want the recorded deferred section (git-independent)", hint3)
	}
	emptyReq := Request{BaseCommit: "abc123", Branch: "wf/wt-test"}
	if hint4 := deliveryInventoryHint(ctx, broken, emptyReq, workflowledger.DeliveryRecord{}); hint4 != "" {
		t.Fatalf("hint = %q with a failing git and no recorded split, want empty", hint4)
	}
}
