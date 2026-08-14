package delivery

// checkChunkScope pins a live e2e finding (smoke-stack-3chunk-v3): every
// chunk of a 3-chunk stack implemented the WHOLE task, and because the
// duplicate implementations used different filenames, git merged two of the
// PRs cleanly and master ended up with two definitions of the same functions
// in one package (a compile error). The delivery boundary must refuse a
// chunk whose staged diff touches files outside the chunk's declared plan
// slice, so the refusal routes to repair instead of publishing.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeliverRefusesChunkTouchingFilesOutsideItsPlanSlice(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)

	writeWorktreeFile(t, worktreeRoot, "declared.txt", "in scope\n")
	writeWorktreeFile(t, worktreeRoot, "undeclared.txt", "OUT of scope\n")

	policy := defaultPolicy("draft")
	policy.StackingHardLines = 500
	inputs := map[string]string{
		"task":       "add declared.txt",
		"chunk_plan": `{"id":"c1","title":"declared only","files":["declared.txt"]}`,
	}
	pr := &fakePRClient{}
	_, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, policy, inputs))
	if err == nil {
		t.Fatal("Deliver succeeded, want a rejection: undeclared.txt is outside the chunk's declared files")
	}
	if IsRefusal(err) {
		t.Fatalf("Deliver error = %v (%T), want a plain repairable error, not a RefusalError (which bypasses the repair step)", err, err)
	}
	if !strings.Contains(err.Error(), "undeclared.txt") {
		t.Fatalf("rejection %q must name the out-of-scope file", err)
	}
	if pr.createdCount() != 0 {
		t.Fatal("a PR was created for an out-of-scope chunk diff")
	}
}

func TestDeliverAllowsChunkWithinItsPlanSlice(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)

	writeWorktreeFile(t, worktreeRoot, "declared.txt", "in scope\n")

	policy := defaultPolicy("draft")
	policy.StackingHardLines = 500
	inputs := map[string]string{
		"task":       "add declared.txt",
		"chunk_plan": `{"id":"c1","title":"declared only","files":["declared.txt"]}`,
	}
	pr := &fakePRClient{}
	res, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, policy, inputs))
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if res.Status != "succeeded" {
		t.Fatalf("Result = %+v, want succeeded", res)
	}
}

// TestDeliverMatchesPlanSliceDeclaredWithNonNormalizedPaths pins the
// normalization finding: chunk_plan.files is agent-authored JSON, and a
// declared "./pkg/file.go" (or a repo-root-absolute "/pkg/file.go") never
// matched git's repo-relative "pkg/file.go" output, so EVERY touched file was
// judged out-of-scope and the run burned its repair budget on an error the
// repair step cannot fix (the plan is host ground truth). The guard must
// normalize both sides the same way.
func TestDeliverMatchesPlanSliceDeclaredWithNonNormalizedPaths(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)

	if err := os.MkdirAll(filepath.Join(worktreeRoot, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeWorktreeFile(t, worktreeRoot, "pkg/file.go", "in scope\n")

	policy := defaultPolicy("draft")
	policy.StackingHardLines = 500
	inputs := map[string]string{
		"task":       "add pkg/file.go",
		"chunk_plan": `{"id":"c1","title":"dot-slash declared","files":["./pkg/file.go"]}`,
	}
	pr := &fakePRClient{}
	res, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, policy, inputs))
	if err != nil {
		t.Fatalf("Deliver: %v (a \"./\"-prefixed declared path is the same file git reports as pkg/file.go)", err)
	}
	if res.Status != "succeeded" {
		t.Fatalf("Result = %+v, want succeeded", res)
	}

	// The repo-root-absolute form must match too.
	writeWorktreeFile(t, worktreeRoot, "pkg/other.go", "also in scope\n")
	inputs["chunk_plan"] = `{"id":"c1","title":"root-absolute declared","files":["/pkg/file.go","/pkg/other.go"]}`
	res, err = Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, policy, inputs))
	if err != nil {
		t.Fatalf("Deliver: %v (a leading-\"/\" declared path is the repo-root file git reports as pkg/file.go)", err)
	}
	if res.Status != "succeeded" {
		t.Fatalf("Result = %+v, want succeeded", res)
	}
}

func TestDeliverSkipsScopeCheckWithoutAChunkPlan(t *testing.T) {
	// Plan/single/integration runs carry no chunk_plan: any file may change.
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "anything.txt", "no scope declared\n")
	policy := defaultPolicy("draft")
	policy.StackingHardLines = 500
	pr := &fakePRClient{}
	if _, err := Deliver(ctx, repo, RealGit{}, pr, newRequest(run, gc, baseCommit, originURL, policy, map[string]string{"task": "x"})); err != nil {
		t.Fatalf("Deliver without chunk_plan: %v", err)
	}
}
