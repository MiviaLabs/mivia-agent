package delivery

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestDeferredPathThatStagesNothingIsRefusedBeforeAnyCommit pins the ordering.
//
// `git reset -- <path not in the index>` exits 0 while `git add -- <same
// path>` exits 128. The split therefore no-opped the reset, CREATED the
// delivered commit, and only then failed on the deferred add - after the split
// decision was durable. On retry the resume path re-ran the same failing add
// before its own path-set check could report the mismatch, so the run could
// not leave the state. The refusal must come first, with the worktree
// untouched.
func TestDeferredPathThatStagesNothingIsRefusedBeforeAnyCommit(t *testing.T) {
	ctx := context.Background()
	_, worktreeRoot, gc, baseCommit, originURL, run, repo := newDeliveryFixture(t)
	writeWorktreeFile(t, worktreeRoot, "real.txt", "a real change\n")

	deferredJSON, err := json.Marshal([]string{"ghost.txt"})
	if err != nil {
		t.Fatal(err)
	}
	inputs := map[string]string{"task": "split delivery", InputDeferredFiles: string(deferredJSON)}
	_, err = Deliver(ctx, repo, RealGit{}, &fakePRClient{}, newRequest(run, gc, baseCommit, originURL, defaultPolicy("draft"), inputs))
	if err == nil {
		t.Fatal("Deliver accepted a deferred path the change does not stage")
	}
	if !strings.Contains(err.Error(), "ghost.txt") {
		t.Fatalf("error %q should name the unstageable deferred path", err)
	}

	// No commit may exist: HEAD must still be the admitted base.
	if head := runGitOut(t, worktreeRoot, "rev-parse", "HEAD"); head != baseCommit {
		t.Fatalf("HEAD = %s, want the untouched base %s: the refusal came after a commit was created", head, baseCommit)
	}
}
