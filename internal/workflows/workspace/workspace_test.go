package workspace

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/vcs"
	baseworkspace "github.com/MiviaLabs/mivia-agent/internal/workspace"
)

func TestEnsureReadOnlyUsesCallerCheckout(t *testing.T) {
	root := initRepo(t)
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := Ensure(context.Background(), nested, "run-read", IsolationReadOnly)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	// workspace.Open resolves the root (filepath.EvalSymlinks), which on
	// Windows also expands 8.3 short names; compare against the same
	// rendering so a short-name temp path does not fail the check.
	if got.Root != baseworkspace.LongPath(nested) {
		t.Fatalf("Root = %q, want %q", got.Root, nested)
	}
	if got.MainRoot != "" || got.BaseCommit != "" || got.WorktreeName != "" || got.Branch != "" {
		t.Fatalf("read-only identity includes worktree data: %+v", got)
	}
}

func TestEnsureReadOnlyDoesNotRequireGit(t *testing.T) {
	root := t.TempDir()
	got, err := Ensure(context.Background(), root, "run-no-git", IsolationReadOnly)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if got != (Identity{Root: baseworkspace.LongPath(root)}) {
		t.Fatalf("identity = %+v, want only Root %q", got, root)
	}
}

func TestEnsureRejectsInvalidAdmission(t *testing.T) {
	root := initRepo(t)
	if _, err := Ensure(context.Background(), root, "run", Isolation(9)); err == nil {
		t.Fatal("Ensure accepts an unknown isolation")
	}
	if _, err := Ensure(context.Background(), root, "", IsolationWorktree); err == nil {
		t.Fatal("Ensure accepts an empty run ID")
	}
	if _, err := Ensure(context.Background(), root, strings.Repeat("x", 80), IsolationWorktree); err == nil {
		t.Fatal("Ensure accepts a worktree name that is too long")
	}
}

func TestWorktreeAdmissionReportsRepositoryAndContextErrors(t *testing.T) {
	if _, err := Ensure(context.Background(), t.TempDir(), "run-no-repo", IsolationWorktree); err == nil {
		t.Fatal("write isolation accepts a non-repository root")
	}
	root := initRepo(t)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := admissionIdentity(canceled, root, "run-canceled"); err == nil {
		t.Fatal("admissionIdentity accepts a canceled context")
	}
	if _, err := admissionIdentity(context.Background(), t.TempDir(), "run-no-main"); err == nil {
		t.Fatal("admissionIdentity accepts a non-repository root")
	}
	bad := Identity{MainRoot: t.TempDir(), BaseCommit: "deadbeef", WorktreeName: "workflow-run-bad", Branch: "wf/workflow-run-bad"}
	if _, err := ensureWorktree(context.Background(), bad); err == nil {
		t.Fatal("ensureWorktree accepts a non-repository main root")
	}
}

func TestWorkflowIdentitySurfacesGitStateErrors(t *testing.T) {
	root := initRepo(t)
	runGit(t, root, "branch", "wf/workflow-run-retained-error", "HEAD")
	retained := Identity{MainRoot: root, BaseCommit: "missing-base", WorktreeName: "workflow-run-retained-error", Branch: "wf/workflow-run-retained-error"}
	if err := validateRetainedBranch(context.Background(), retained); err == nil {
		t.Fatal("retained branch accepts a missing base")
	}
	if _, err := validateWorktree(context.Background(), Identity{MainRoot: t.TempDir(), BaseCommit: "deadbeef", WorktreeName: "workflow-run", Branch: "wf/workflow-run"}); err == nil {
		t.Fatal("validateWorktree accepts a non-repository root")
	}

	identity, err := Ensure(context.Background(), root, "run-git-errors", IsolationWorktree)
	if err != nil {
		t.Fatal(err)
	}
	badBase := identity
	badBase.BaseCommit = "missing-base"
	if _, err := validateWorktree(context.Background(), badBase); err == nil {
		t.Fatal("validateWorktree accepts a missing base")
	}
	if err := os.RemoveAll(identity.Root); err != nil {
		t.Fatal(err)
	}
	if _, err := validateWorktree(context.Background(), identity); err == nil {
		t.Fatal("validateWorktree accepts a missing checkout directory")
	}
}

func TestAdmissionIdentitySurfacesCommitFailure(t *testing.T) {
	original := workflowCurrentCommit
	workflowCurrentCommit = func(context.Context, string) (string, error) { return "", errors.New("commit failure") }
	t.Cleanup(func() { workflowCurrentCommit = original })
	if _, err := admissionIdentity(context.Background(), initRepo(t), "run-commit-error"); err == nil || !strings.Contains(err.Error(), "base commit") {
		t.Fatalf("error = %v, want base commit failure", err)
	}
}

func TestEnsureWorktreeRecoversCreateRace(t *testing.T) {
	root := initRepo(t)
	commit, err := vcs.CurrentCommit(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	identity := Identity{MainRoot: root, BaseCommit: commit, WorktreeName: "workflow-run-race", Branch: "wf/workflow-run-race"}
	resolved := 0
	originalResolve, originalCreate := workflowResolve, workflowCreate
	workflowResolve = func(context.Context, string, string) (*vcs.WorktreeInfo, error) {
		resolved++
		if resolved == 1 {
			return nil, nil
		}
		return &vcs.WorktreeInfo{Name: identity.WorktreeName, Path: root, Branch: identity.Branch}, nil
	}
	workflowCreate = func(context.Context, string, string, string, string) (*vcs.WorktreeInfo, error) {
		return nil, errors.New("create race")
	}
	t.Cleanup(func() { workflowResolve, workflowCreate = originalResolve, originalCreate })
	got, err := ensureWorktree(context.Background(), identity)
	if err != nil || got.Root != root {
		t.Fatalf("identity = %+v, error = %v", got, err)
	}
}

func TestEnsureWorktreeReportsValidationAndCleanupErrors(t *testing.T) {
	for _, cleanupFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "validation", true: "cleanup"}[cleanupFails], func(t *testing.T) {
			root := initRepo(t)
			commit, _ := vcs.CurrentCommit(context.Background(), root)
			identity := Identity{MainRoot: root, BaseCommit: commit, WorktreeName: "workflow-run-cleanup", Branch: "wf/workflow-run-cleanup"}
			originalResolve, originalCreate, originalRemove := workflowResolve, workflowCreate, workflowRemove
			workflowResolve = func(context.Context, string, string) (*vcs.WorktreeInfo, error) { return nil, nil }
			workflowCreate = func(context.Context, string, string, string, string) (*vcs.WorktreeInfo, error) {
				return &vcs.WorktreeInfo{Name: identity.WorktreeName, Path: root, Branch: identity.Branch}, nil
			}
			workflowRemove = func(context.Context, string, string, string) error {
				if cleanupFails {
					return errors.New("cleanup failure")
				}
				return nil
			}
			t.Cleanup(func() {
				workflowResolve, workflowCreate, workflowRemove = originalResolve, originalCreate, originalRemove
			})
			_, err := ensureWorktree(context.Background(), identity)
			if err == nil {
				t.Fatal("ensureWorktree succeeded after validation failure")
			}
			if cleanupFails && !strings.Contains(err.Error(), "cleanup") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestEnsureWorktreeRejectsMissingBaseCommit(t *testing.T) {
	root := initRepo(t)
	identity := Identity{MainRoot: root, BaseCommit: "missing", WorktreeName: "workflow-run-missing-base", Branch: "wf/workflow-run-missing-base"}
	if _, err := ensureWorktree(context.Background(), identity); err == nil {
		t.Fatal("ensureWorktree accepts a missing base commit")
	}
}

func TestEnsureCreatesWorktreeFromExactCallerCommit(t *testing.T) {
	root := initRepo(t)
	runGit(t, root, "switch", "-c", "feature/caller")
	writeFile(t, root, "feature.txt", "feature")
	runGit(t, root, "add", "feature.txt")
	runGit(t, root, "commit", "-m", "feature")
	wantCommit, err := vcs.CurrentCommit(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}

	got, err := Ensure(context.Background(), root, "run-123", IsolationWorktree)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if got.MainRoot != root || got.BaseRef != "feature/caller" || got.BaseCommit != wantCommit {
		t.Fatalf("identity = %+v", got)
	}
	if got.WorktreeName != "workflow-run-123" || got.Branch != "wf/workflow-run-123" {
		t.Fatalf("worktree identity = %+v", got)
	}
	worktreeCommit, err := vcs.CurrentCommit(context.Background(), got.Root)
	if err != nil {
		t.Fatal(err)
	}
	if worktreeCommit != wantCommit {
		t.Fatalf("worktree commit = %q, want %q", worktreeCommit, wantCommit)
	}
}

func TestEnsureRejectsIncompatibleRetainedBranchBeforeAttach(t *testing.T) {
	root := initRepo(t)
	runGit(t, root, "branch", "wf/workflow-run-old", "HEAD")
	writeFile(t, root, "next.txt", "next")
	runGit(t, root, "add", "next.txt")
	runGit(t, root, "commit", "-m", "next")

	if _, err := Ensure(context.Background(), root, "run-old", IsolationWorktree); err == nil {
		t.Fatal("Ensure accepts a retained branch that does not contain BaseCommit")
	}
	got, err := vcs.Resolve(context.Background(), root, "workflow-run-old")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("Ensure attaches incompatible retained branch: %+v", got)
	}
}

func TestEnsureAttachesCompatibleRetainedBranch(t *testing.T) {
	root := initRepo(t)
	runGit(t, root, "branch", "wf/workflow-run-retained", "HEAD")
	got, err := Ensure(context.Background(), root, "run-retained", IsolationWorktree)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if got.Branch != "wf/workflow-run-retained" {
		t.Fatalf("Branch = %q", got.Branch)
	}
}

func TestEnsureIsIdempotentDuringConcurrentCalls(t *testing.T) {
	root := initRepo(t)
	const count = 8
	results := make([]Identity, count)
	errs := make([]error, count)
	var group sync.WaitGroup
	for i := range count {
		group.Add(1)
		go func() {
			defer group.Done()
			results[i], errs[i] = Ensure(context.Background(), root, "run-race", IsolationWorktree)
		}()
	}
	group.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("Ensure %d: %v", i, err)
		}
		if results[i] != results[0] {
			t.Fatalf("result %d = %+v, want %+v", i, results[i], results[0])
		}
	}
}

func TestResolveChecksExactBranchAndBaseAncestry(t *testing.T) {
	t.Run("branch", func(t *testing.T) {
		root := initRepo(t)
		identity, err := Ensure(context.Background(), root, "run-branch", IsolationWorktree)
		if err != nil {
			t.Fatal(err)
		}
		runGit(t, identity.Root, "switch", "-c", "other/branch")
		if _, err := Resolve(context.Background(), root, identity); err == nil {
			t.Fatal("Resolve accepts a different branch")
		}
	})

	t.Run("ancestry", func(t *testing.T) {
		root := initRepo(t)
		writeFile(t, root, "next.txt", "next")
		runGit(t, root, "add", "next.txt")
		runGit(t, root, "commit", "-m", "next")
		identity, err := Ensure(context.Background(), root, "run-ancestry", IsolationWorktree)
		if err != nil {
			t.Fatal(err)
		}
		runGit(t, identity.Root, "reset", "--hard", "HEAD^")
		if _, err := Resolve(context.Background(), root, identity); err == nil {
			t.Fatal("Resolve accepts a commit before BaseCommit")
		}
	})
}

func TestResolveReturnsRecordedWorktree(t *testing.T) {
	root := initRepo(t)
	identity, err := Ensure(context.Background(), root, "run-resolve", IsolationWorktree)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Resolve(context.Background(), root, identity)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != identity {
		t.Fatalf("Resolve = %+v, want %+v", got, identity)
	}
}

func TestResolveRejectsInvalidRecordedIdentity(t *testing.T) {
	root := initRepo(t)
	identity, err := Ensure(context.Background(), root, "run-invalid", IsolationWorktree)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		change func(*Identity)
	}{
		{"main root", func(got *Identity) { got.MainRoot = t.TempDir() }},
		{"name", func(got *Identity) { got.WorktreeName = "../invalid" }},
		{"branch", func(got *Identity) { got.Branch = "wf/different" }},
		{"base commit", func(got *Identity) { got.BaseCommit = "" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invalid := identity
			test.change(&invalid)
			if _, err := Resolve(context.Background(), root, invalid); err == nil {
				t.Fatal("Resolve accepts an invalid identity")
			}
		})
	}

	missing := identity
	missing.WorktreeName = "workflow-run-missing"
	missing.Branch = "wf/workflow-run-missing"
	if _, err := Resolve(context.Background(), root, missing); err == nil {
		t.Fatal("Resolve accepts a missing worktree")
	}
}

func TestResolveReadOnlyRejectsFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(context.Background(), file, Identity{}); err == nil {
		t.Fatal("Resolve accepts a file as a read-only root")
	}
}

func TestResolveRejectsMixedReadOnlyIdentity(t *testing.T) {
	root := t.TempDir()
	tests := []Identity{
		{MainRoot: root},
		{BaseRef: "main"},
		{BaseCommit: "deadbeef"},
		{Branch: "wf/workflow-run"},
	}
	for _, identity := range tests {
		if _, err := Resolve(context.Background(), root, identity); err == nil {
			t.Fatalf("Resolve accepts mixed read-only identity: %+v", identity)
		}
	}
}

func TestResolveWorktreeRequiresRepository(t *testing.T) {
	recorded := Identity{BaseRef: "main", BaseCommit: "deadbeef", WorktreeName: "workflow-run", Branch: "wf/workflow-run"}
	if _, err := Resolve(context.Background(), t.TempDir(), recorded); err == nil {
		t.Fatal("Resolve accepts a recorded worktree outside a repository")
	}
}

func TestAdmissionRecordsOriginBaseCommit(t *testing.T) {
	// Local master is ahead of origin/master: origin/master sits at commit A,
	// local master at commit B. Admission must record BOTH the local HEAD as
	// BaseCommit AND the origin tracking ref as OriginBaseCommit, so delivery
	// can later verify against the remote base instead of the local branch.
	bare := t.TempDir()
	runGit(t, bare, "init", "--bare")

	root := initRepo(t) // master at commit A
	runGit(t, root, "branch", "-m", "master")
	runGit(t, root, "remote", "add", "origin", bare)
	runGit(t, root, "push", "-u", "origin", "master")
	runGit(t, root, "fetch", "origin") // guarantee refs/remotes/origin/master exists

	originCommit, err := vcs.ResolveCommit(context.Background(), root, "refs/remotes/origin/master")
	if err != nil {
		t.Fatal(err)
	}

	writeFile(t, root, "next.txt", "next")
	runGit(t, root, "add", "next.txt")
	runGit(t, root, "commit", "-m", "next")
	localCommit, err := vcs.CurrentCommit(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if localCommit == originCommit {
		t.Fatal("precondition: local master must be ahead of origin/master")
	}

	identity, err := Ensure(context.Background(), root, "run-origin-base", IsolationWorktree)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if identity.BaseCommit != localCommit {
		t.Fatalf("BaseCommit = %q, want local HEAD %q", identity.BaseCommit, localCommit)
	}
	if identity.OriginBaseCommit != originCommit {
		t.Fatalf("OriginBaseCommit = %q, want origin/master %q", identity.OriginBaseCommit, originCommit)
	}
}

func TestAdmissionOriginBaseCommitEmptyWithoutRemoteRef(t *testing.T) {
	root := initRepo(t) // no origin remote, so refs/remotes/origin/<base> is absent
	identity, err := Ensure(context.Background(), root, "run-no-origin", IsolationWorktree)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if identity.OriginBaseCommit != "" {
		t.Fatalf("OriginBaseCommit = %q, want empty when no remote ref is present", identity.OriginBaseCommit)
	}
}

func TestAdmissionDetachedHEADRecordsEmptyOriginBaseCommit(t *testing.T) {
	bare := t.TempDir()
	runGit(t, bare, "init", "--bare")

	root := initRepo(t)
	runGit(t, root, "branch", "-m", "master", "main")
	runGit(t, root, "remote", "add", "origin", bare)
	runGit(t, root, "push", "-u", "origin", "main")

	// Create a develop branch on the remote and make origin/HEAD point to it.
	// This mirrors a clone where the remote default branch is develop,
	// while the workflow base branch is main.
	runGit(t, root, "checkout", "-b", "develop")
	writeFile(t, root, "develop.txt", "develop")
	runGit(t, root, "add", "develop.txt")
	runGit(t, root, "commit", "-m", "develop")
	runGit(t, root, "push", "-u", "origin", "develop")
	runGit(t, root, "fetch", "origin")
	runGit(t, root, "remote", "set-head", "origin", "develop")

	// Detach at the main tip.
	runGit(t, root, "checkout", "--detach", "main")
	detachedCommit, err := vcs.CurrentCommit(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}

	identity, err := Ensure(context.Background(), root, "run-detached", IsolationWorktree)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if identity.BaseRef != "HEAD" {
		t.Fatalf("BaseRef = %q, want HEAD", identity.BaseRef)
	}
	if identity.BaseCommit != detachedCommit {
		t.Fatalf("BaseCommit = %q, want detached commit %q", identity.BaseCommit, detachedCommit)
	}
	if identity.OriginBaseCommit != "" {
		t.Fatalf("OriginBaseCommit = %q, want empty on detached HEAD", identity.OriginBaseCommit)
	}
}

// TestEnsureIgnoresAmbientGitDir is the DC-10 regression test for workflow
// workspace discovery: a workflow launched from a git hook or CI job inherits
// GIT_DIR/GIT_WORK_TREE pointing at a different repository. Ensure must still
// resolve and create the worktree under the source repository. Fails before
// the pinnedEnv fix in internal/vcs.
func TestEnsureIgnoresAmbientGitDir(t *testing.T) {
	source := initRepo(t)
	other := initRepo(t)
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_WORK_TREE", other)

	got, err := Ensure(context.Background(), source, "run-ambient", IsolationWorktree)
	if err != nil {
		t.Fatalf("Ensure with ambient GIT_DIR/GIT_WORK_TREE: %v", err)
	}
	abs, _ := filepath.Abs(source)
	if got.MainRoot != abs {
		t.Errorf("MainRoot = %q, want source repo %q (ambient GIT_DIR must not redirect)", got.MainRoot, abs)
	}
	if got.WorktreeName != "workflow-run-ambient" {
		t.Errorf("WorktreeName = %q, want workflow-run-ambient", got.WorktreeName)
	}
	if !strings.HasPrefix(got.Root, abs+string(filepath.Separator)) {
		t.Errorf("worktree Root = %q, want under source repo %q", got.Root, abs)
	}
	commit, err := vcs.CurrentCommit(context.Background(), got.Root)
	if err != nil {
		t.Fatalf("CurrentCommit in the ensured worktree: %v", err)
	}
	if commit != got.BaseCommit {
		t.Errorf("worktree commit = %q, want base commit %q", commit, got.BaseCommit)
	}
}

func TestValidateRetainedBranchHonorsCanceledContext(t *testing.T) {
	root := initRepo(t)
	identity := Identity{MainRoot: root, BaseCommit: "HEAD", WorktreeName: "workflow-run-canceled", Branch: "wf/workflow-run-canceled"}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := validateRetainedBranch(canceled, identity); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func initRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "test@example.com")
	runGit(t, root, "config", "user.name", "Test")
	writeFile(t, root, "README.md", "test")
	runGit(t, root, "add", "README.md")
	runGit(t, root, "commit", "-m", "init")
	return root
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func writeFile(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
