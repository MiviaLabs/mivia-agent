package delivery

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// admitFixtureRepo builds a real git repo with a bare origin, one base
// commit on branch base pushed to origin, and returns everything a caller
// needs to check out further branches on top of it.
func admitFixtureRepo(t *testing.T, base string) (repoRoot, originURL string) {
	t.Helper()
	repoRoot = t.TempDir()
	runGit(t, repoRoot, "init", "-b", base)
	gitConfig(t, repoRoot)
	writeWorktreeFile(t, repoRoot, "a.txt", "base\n")
	runGit(t, repoRoot, "add", "a.txt")
	runGit(t, repoRoot, "commit", "-m", "base")

	originDir := filepath.Join(t.TempDir(), "origin.git")
	runGit(t, filepath.Dir(originDir), "init", "--bare", filepath.Base(originDir))
	runGit(t, repoRoot, "remote", "add", "origin", originDir)
	runGit(t, repoRoot, "push", "-u", "origin", base)
	return repoRoot, originDir
}

// TestAdmitDeliveryTargetSameBranchScenarios pins the containment invariant
// for the same-branch cases of the four scenarios that motivate replacing
// admission's old equality check (target ref must be at the EXACT SAME
// commit as the worktree base) with containment (the target must CONTAIN
// the worktree base as an ancestor).
func TestAdmitDeliveryTargetSameBranchScenarios(t *testing.T) {
	t.Run("main to main", func(t *testing.T) {
		repoRoot, originURL := admitFixtureRepo(t, "main")
		worktreeBase := runGitOut(t, repoRoot, "rev-parse", "HEAD")
		gc := GitContext{Dir: repoRoot, GitDir: filepath.Join(repoRoot, ".git")}
		originResolved, target, err := AdmitDeliveryTarget(context.Background(), RealGit{}, gc, "main", worktreeBase)
		if err != nil {
			t.Fatalf("AdmitDeliveryTarget = %v, want success (self-ancestor)", err)
		}
		if originResolved != originURL {
			t.Fatalf("originURL = %q, want %q", originResolved, originURL)
		}
		if target != worktreeBase {
			t.Fatalf("targetOriginCommit = %q, want %q", target, worktreeBase)
		}
	})

	t.Run("dev to dev", func(t *testing.T) {
		repoRoot, _ := admitFixtureRepo(t, "dev")
		worktreeBase := runGitOut(t, repoRoot, "rev-parse", "HEAD")
		gc := GitContext{Dir: repoRoot, GitDir: filepath.Join(repoRoot, ".git")}
		if _, _, err := AdmitDeliveryTarget(context.Background(), RealGit{}, gc, "dev", worktreeBase); err != nil {
			t.Fatalf("AdmitDeliveryTarget(dev->dev) = %v, want success", err)
		}
	})

	t.Run("target does not exist on remote refused", func(t *testing.T) {
		repoRoot, _ := admitFixtureRepo(t, "main")
		worktreeBase := runGitOut(t, repoRoot, "rev-parse", "HEAD")
		gc := GitContext{Dir: repoRoot, GitDir: filepath.Join(repoRoot, ".git")}
		_, _, err := AdmitDeliveryTarget(context.Background(), RealGit{}, gc, "release", worktreeBase)
		if err == nil {
			t.Fatal("AdmitDeliveryTarget must refuse a target branch absent from the remote")
		}
	})
}

// TestAdmitDeliveryTargetCrossBranchScenarios pins the cross-branch cases: a
// feature branch forked from the target admits when its base commit is the
// fork point, and refuses when its own tip or an unrelated/unmerged branch
// is the base, since a PR into the target cannot carry exactly that diff.
func TestAdmitDeliveryTargetCrossBranchScenarios(t *testing.T) {
	t.Run("feature cut from dev to dev", func(t *testing.T) {
		repoRoot, _ := admitFixtureRepo(t, "dev")
		// Advance dev with one more commit and push it, then fork a feature
		// branch from that tip and add a further LOCAL, unpushed commit.
		// The worktree's base commit is the feature branch's own tip, which
		// the target does NOT contain (nothing of feature-x is on dev yet):
		// admission must refuse, since a PR into dev cannot carry exactly
		// this run's diff.
		writeWorktreeFile(t, repoRoot, "b.txt", "dev advance\n")
		runGit(t, repoRoot, "add", "b.txt")
		runGit(t, repoRoot, "commit", "-m", "advance dev")
		runGit(t, repoRoot, "push", "origin", "dev")
		runGit(t, repoRoot, "checkout", "-b", "feature-x")
		writeWorktreeFile(t, repoRoot, "c.txt", "feature work\n")
		runGit(t, repoRoot, "add", "c.txt")
		runGit(t, repoRoot, "commit", "-m", "feature work")
		worktreeBase := runGitOut(t, repoRoot, "rev-parse", "HEAD")
		gc := GitContext{Dir: repoRoot, GitDir: filepath.Join(repoRoot, ".git")}
		_, _, err := AdmitDeliveryTarget(context.Background(), RealGit{}, gc, "dev", worktreeBase)
		if err == nil {
			t.Fatal("AdmitDeliveryTarget must refuse: feature-x's own tip is not yet on dev")
		}
	})

	t.Run("feature forked from dev, base commit is the fork point", func(t *testing.T) {
		repoRoot, _ := admitFixtureRepo(t, "dev")
		devTip := runGitOut(t, repoRoot, "rev-parse", "HEAD")
		runGit(t, repoRoot, "checkout", "-b", "feature-y")
		// The worktree's base commit is the fork point itself (as it is at
		// admission, before the run's own agent commits anything): still on
		// dev, so containment holds without any fetch of feature-y at all.
		gc := GitContext{Dir: repoRoot, GitDir: filepath.Join(repoRoot, ".git")}
		if _, _, err := AdmitDeliveryTarget(context.Background(), RealGit{}, gc, "dev", devTip); err != nil {
			t.Fatalf("AdmitDeliveryTarget(fork point->dev) = %v, want success", err)
		}
	})

	t.Run("dev to main unmerged refused", func(t *testing.T) {
		repoRoot := t.TempDir()
		runGit(t, repoRoot, "init", "-b", "main")
		gitConfig(t, repoRoot)
		writeWorktreeFile(t, repoRoot, "a.txt", "base\n")
		runGit(t, repoRoot, "add", "a.txt")
		runGit(t, repoRoot, "commit", "-m", "base")
		originDir := filepath.Join(t.TempDir(), "origin.git")
		runGit(t, filepath.Dir(originDir), "init", "--bare", filepath.Base(originDir))
		runGit(t, repoRoot, "remote", "add", "origin", originDir)
		runGit(t, repoRoot, "push", "-u", "origin", "main")
		runGit(t, repoRoot, "checkout", "-b", "dev")
		writeWorktreeFile(t, repoRoot, "d.txt", "dev only\n")
		runGit(t, repoRoot, "add", "d.txt")
		runGit(t, repoRoot, "commit", "-m", "dev only, never merged")
		runGit(t, repoRoot, "push", "-u", "origin", "dev")
		worktreeBase := runGitOut(t, repoRoot, "rev-parse", "HEAD")

		gc := GitContext{Dir: repoRoot, GitDir: filepath.Join(repoRoot, ".git")}
		_, _, err := AdmitDeliveryTarget(context.Background(), RealGit{}, gc, "main", worktreeBase)
		if err == nil {
			t.Fatal("AdmitDeliveryTarget(dev->main) must refuse: main does not yet contain dev's unmerged tip")
		}
	})
}

// TestAdmitDeliveryTargetLocalAheadOfOriginAccepted pins the local-ahead-of-
// origin fallback: an operator who committed to the target branch locally
// but has not yet pushed it must still admit, via the local refs/heads/<base>
// fallback - the primary fetched-origin-tip check alone would wrongly refuse
// this ordinary local-development state.
func TestAdmitDeliveryTargetLocalAheadOfOriginAccepted(t *testing.T) {
	repoRoot, _ := admitFixtureRepo(t, "main")
	writeWorktreeFile(t, repoRoot, "next.txt", "local ahead\n")
	runGit(t, repoRoot, "add", "next.txt")
	runGit(t, repoRoot, "commit", "-m", "local ahead of origin")
	worktreeBase := runGitOut(t, repoRoot, "rev-parse", "HEAD")
	originTip := runGitOut(t, repoRoot, "rev-parse", "refs/remotes/origin/main")
	if worktreeBase == originTip {
		t.Fatal("precondition: local main must be ahead of origin/main")
	}

	gc := GitContext{Dir: repoRoot, GitDir: filepath.Join(repoRoot, ".git")}
	_, target, err := AdmitDeliveryTarget(context.Background(), RealGit{}, gc, "main", worktreeBase)
	if err != nil {
		t.Fatalf("AdmitDeliveryTarget = %v, want success via the local-ref fallback", err)
	}
	// The recorded pin is always the fetched ORIGIN tip, never the local
	// fallback ref: delivery-time rewrite detection needs the remote's own
	// history, not this checkout's unpushed state.
	if target != originTip {
		t.Fatalf("targetOriginCommit = %q, want the origin tip %q (not the local-ahead tip)", target, originTip)
	}
}

// TestAdmitDeliveryTargetOriginAdvancedPastForkPoint pins STRICT (non-
// reflexive) containment - the mechanism the whole fix exists for: the target
// advanced on origin past the run's fork point BEFORE admission. The fetched
// origin tip strictly contains the worktree base commit, so admission must
// succeed and pin the ADVANCED tip. Every other green admission in this file
// compares two equal SHAs (reflexive ancestry), which a plain equality check
// would also pass; this is the only subtest that distinguishes containment
// from equality.
func TestAdmitDeliveryTargetOriginAdvancedPastForkPoint(t *testing.T) {
	repoRoot, _ := admitFixtureRepo(t, "dev")
	forkPoint := runGitOut(t, repoRoot, "rev-parse", "HEAD")
	// Advance dev past the fork point and PUSH the advance before admission.
	writeWorktreeFile(t, repoRoot, "b.txt", "dev advance\n")
	runGit(t, repoRoot, "add", "b.txt")
	runGit(t, repoRoot, "commit", "-m", "advance dev")
	runGit(t, repoRoot, "push", "origin", "dev")
	advancedTip := runGitOut(t, repoRoot, "rev-parse", "HEAD")
	if advancedTip == forkPoint {
		t.Fatal("precondition: dev must have advanced past the fork point")
	}

	gc := GitContext{Dir: repoRoot, GitDir: filepath.Join(repoRoot, ".git")}
	_, target, err := AdmitDeliveryTarget(context.Background(), RealGit{}, gc, "dev", forkPoint)
	if err != nil {
		t.Fatalf("AdmitDeliveryTarget(advanced origin, fork point) = %v, want strict containment to admit", err)
	}
	if target != advancedTip {
		t.Fatalf("targetOriginCommit = %q, want the advanced origin tip %q", target, advancedTip)
	}
}

// TestAdmitDeliveryTargetDivergedLocalRefused pins the fallback's boundary:
// the local-refs/heads/<base> fallback covers exactly "local target strictly
// AHEAD of origin" (operator committed locally, not yet pushed). Any
// DIVERGENCE between the local target ref and the fetched origin tip must
// refuse: admitting it would record a pin unrelated to the worktree base, and
// every delivery-time rewrite check compares that pin against itself, so a
// target rewrite would go undetected and the PR would re-publish commits the
// remote dropped.
func TestAdmitDeliveryTargetDivergedLocalRefused(t *testing.T) {
	t.Run("local and origin advanced on different lines", func(t *testing.T) {
		repoRoot, originURL := admitFixtureRepo(t, "main")
		// Local advance, never pushed: the would-be worktree base commit.
		writeWorktreeFile(t, repoRoot, "local.txt", "local line\n")
		runGit(t, repoRoot, "add", "local.txt")
		runGit(t, repoRoot, "commit", "-m", "local advance")
		worktreeBase := runGitOut(t, repoRoot, "rev-parse", "HEAD")
		// Origin advances on a DIFFERENT line (a second clone pushes).
		cloneDir := filepath.Join(t.TempDir(), "clone")
		runGit(t, filepath.Dir(cloneDir), "clone", "-q", originURL, cloneDir)
		gitConfig(t, cloneDir)
		// The bare origin's HEAD may not resolve to main, so take main
		// explicitly from the fetched ref before committing on it.
		runGit(t, cloneDir, "checkout", "-q", "-B", "main", "origin/main")
		writeWorktreeFile(t, cloneDir, "remote.txt", "remote line\n")
		runGit(t, cloneDir, "add", "remote.txt")
		runGit(t, cloneDir, "commit", "-m", "remote advance")
		runGit(t, cloneDir, "push", "-q", "origin", "main")

		gc := GitContext{Dir: repoRoot, GitDir: filepath.Join(repoRoot, ".git")}
		if _, _, err := AdmitDeliveryTarget(context.Background(), RealGit{}, gc, "main", worktreeBase); err == nil {
			t.Fatal("AdmitDeliveryTarget must refuse: local main and origin/main have diverged")
		}
	})

	t.Run("origin rewritten while local clone stale", func(t *testing.T) {
		repoRoot, originURL := admitFixtureRepo(t, "main")
		worktreeBase := runGitOut(t, repoRoot, "rev-parse", "HEAD")
		// Force-push a rewrite that DROPS the commit the run started from;
		// the local refs/heads/main still points at the dropped commit.
		rewriteRemoteBase(t, repoRoot, originURL)

		gc := GitContext{Dir: repoRoot, GitDir: filepath.Join(repoRoot, ".git")}
		if _, _, err := AdmitDeliveryTarget(context.Background(), RealGit{}, gc, "main", worktreeBase); err == nil {
			t.Fatal("AdmitDeliveryTarget must refuse: origin/main was rewritten and no longer contains the worktree base")
		}
	})
}

// TestAdmitDeliveryTargetThenDeliverRefusesAfterRewrite is the two-phase
// admission-to-delivery regression: AdmitDeliveryTarget's own resolved pin
// (not an empty/fallback one) must still catch a target branch rewritten
// after admission.
func TestAdmitDeliveryTargetThenDeliverRefusesAfterRewrite(t *testing.T) {
	ctx := context.Background()
	repoRoot, originURL := admitFixtureRepo(t, "main")
	baseCommit := runGitOut(t, repoRoot, "rev-parse", "HEAD")
	gc := GitContext{Dir: repoRoot, GitDir: filepath.Join(repoRoot, ".git")}

	_, originBase, err := AdmitDeliveryTarget(ctx, RealGit{}, gc, "main", baseCommit)
	if err != nil {
		t.Fatalf("AdmitDeliveryTarget: %v", err)
	}
	if originBase != baseCommit {
		t.Fatalf("originBase = %q, want %q", originBase, baseCommit)
	}

	worktreeRoot := filepath.Join(t.TempDir(), "wt")
	runGit(t, repoRoot, "worktree", "add", "-b", "wf/wt-test", worktreeRoot, baseCommit)
	gitConfig(t, worktreeRoot)
	wtGC := GitContext{Dir: worktreeRoot, GitDir: filepath.Join(repoRoot, ".git", "worktrees", filepath.Base(worktreeRoot))}

	rewriteRemoteBase(t, repoRoot, originURL)

	store := storage.NewMemory()
	repo := workflowledger.NewStorageRepository(store)
	run := createRunWithStatus(t, repo, workflowledger.RunSnapshot{
		RunID: "wfr-test", WorkflowName: "test-wf", WorkflowDigest: "digest",
		ActiveStepID: "success", RemoteURL: originURL,
		OriginBaseCommit: originBase,
	}, workflowledger.RunStatusDeliveryPending)

	writeWorktreeFile(t, worktreeRoot, "b.txt", "change\n")
	pr := &fakePRClient{}
	_, err = Deliver(ctx, repo, RealGit{}, pr, newRequest(run, wtGC, baseCommit, originURL, defaultPolicy("draft"), map[string]string{"task": "x"}))
	if err == nil || !IsRefusal(err) {
		t.Fatalf("Deliver err = %v, want RefusalError for a target rewritten after admission", err)
	}
}
