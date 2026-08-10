package delivery

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRealGitRunsWithPinnedEnv(t *testing.T) {
	repo := initRepo(t)
	bare := t.TempDir()
	runGit(t, bare, "init", "--bare")
	runGit(t, repo, "remote", "add", "origin", bare)

	runner := GitRunner(RealGit{})
	gc := GitContext{Dir: repo, GitDir: filepath.Join(repo, ".git")}

	got, err := runner.Run(context.Background(), gc, "remote", "get-url", "origin")
	if err != nil {
		t.Fatalf("remote get-url: %v", err)
	}
	if url := strings.TrimSpace(got); url != bare {
		t.Fatalf("remote URL = %q, want %q", url, bare)
	}

	got, err = runner.Run(context.Background(), gc, "rev-parse", "--show-toplevel")
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	top := filepath.Clean(filepath.FromSlash(strings.TrimSpace(got)))
	want, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	if top != want {
		t.Fatalf("toplevel = %q, want %q", top, want)
	}
}

func TestRealGitNeutralizesAmbientGitEnv(t *testing.T) {
	repo := initRepo(t)
	other := t.TempDir()
	t.Setenv("GIT_DIR", other)
	t.Setenv("GIT_WORK_TREE", other)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(other, "index"))
	t.Setenv("GIT_SSH_COMMAND", "echo hijacked")

	gc := GitContext{Dir: repo, GitDir: filepath.Join(repo, ".git")}
	got, err := (RealGit{}).Run(context.Background(), gc, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		t.Fatalf("rev-parse: %v\n%s", err, got)
	}
	if out := strings.TrimSpace(got); out != "true" {
		t.Fatalf("is-inside-work-tree = %q, want %q", out, "true")
	}
}

func TestRealGitPinsCommitIdentity(t *testing.T) {
	repo := initRepo(t)
	// Ambient author/committer variables must never leak into a delivery
	// commit: the host owns the identity of what it publishes.
	t.Setenv("GIT_AUTHOR_NAME", "evil-author")
	t.Setenv("GIT_AUTHOR_EMAIL", "evil@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "evil-committer")
	t.Setenv("GIT_COMMITTER_EMAIL", "evil@example.com")

	gc := GitContext{Dir: repo, GitDir: filepath.Join(repo, ".git")}
	base, err := (RealGit{}).Run(context.Background(), gc, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "b.txt"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := (RealGit{}).Run(context.Background(), gc, "add", "b.txt"); err != nil {
		t.Fatalf("git add: %v", err)
	}
	tree, err := (RealGit{}).Run(context.Background(), gc, "write-tree")
	if err != nil {
		t.Fatalf("git write-tree: %v", err)
	}
	sha, err := (RealGit{}).Run(context.Background(), gc,
		"-c", "user.name=mivia", "-c", "user.email=mivia@localhost",
		"commit-tree", strings.TrimSpace(tree), "-p", strings.TrimSpace(base), "-m", "delivery")
	if err != nil {
		t.Fatalf("git commit-tree: %v", err)
	}
	author, err := (RealGit{}).Run(context.Background(), gc, "log", "-1", "--format=%an <%ae>", strings.TrimSpace(sha))
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	if got := strings.TrimSpace(author); got != "mivia <mivia@localhost>" {
		t.Fatalf("commit author = %q, want %q (ambient identity leaked)", got, "mivia <mivia@localhost>")
	}
}

func TestVerifyGitDir(t *testing.T) {
	t.Run("valid worktree", func(t *testing.T) {
		mainRoot, name, worktreeDir := addWorktree(t)
		got, err := VerifyGitDir(context.Background(), mainRoot, name, worktreeDir)
		if err != nil {
			t.Fatalf("VerifyGitDir: %v", err)
		}
		want := filepath.Join(mainRoot, ".git", "worktrees", name)
		if got != want {
			t.Fatalf("git dir = %q, want %q", got, want)
		}
	})

	t.Run("relative gitdir accepted", func(t *testing.T) {
		mainRoot, name, worktreeDir := addWorktree(t)
		gitDir := filepath.Join(mainRoot, ".git", "worktrees", name)
		rel, err := filepath.Rel(worktreeDir, gitDir)
		if err != nil {
			t.Fatal(err)
		}
		writeDotGit(t, worktreeDir, "gitdir: "+rel+"\n")
		got, err := VerifyGitDir(context.Background(), mainRoot, name, worktreeDir)
		if err != nil {
			t.Fatalf("VerifyGitDir: %v", err)
		}
		if got != gitDir {
			t.Fatalf("git dir = %q, want %q", got, gitDir)
		}
	})

	t.Run("missing .git refused", func(t *testing.T) {
		if _, err := VerifyGitDir(context.Background(), t.TempDir(), "wt", t.TempDir()); err == nil {
			t.Fatal("VerifyGitDir accepts a worktree without a .git file")
		}
	})

	t.Run("directory .git refused", func(t *testing.T) {
		mainRoot := initRepo(t)
		if _, err := VerifyGitDir(context.Background(), mainRoot, "main", mainRoot); err == nil {
			t.Fatal("VerifyGitDir accepts a .git directory")
		}
	})

	t.Run("symlinked .git refused", func(t *testing.T) {
		mainRoot, name, worktreeDir := addWorktree(t)
		gitDir := filepath.Join(mainRoot, ".git", "worktrees", name)
		if err := os.Remove(filepath.Join(worktreeDir, ".git")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(gitDir, filepath.Join(worktreeDir, ".git")); err != nil {
			t.Fatal(err)
		}
		if _, err := VerifyGitDir(context.Background(), mainRoot, name, worktreeDir); err == nil {
			t.Fatal("VerifyGitDir accepts a symlinked .git file")
		}
	})

	t.Run("misdirected gitdir refused", func(t *testing.T) {
		worktreeDir := t.TempDir()
		writeDotGit(t, worktreeDir, "gitdir: "+filepath.Join(t.TempDir(), "other")+"\n")
		if _, err := VerifyGitDir(context.Background(), t.TempDir(), "wt", worktreeDir); err == nil {
			t.Fatal("VerifyGitDir accepts a misdirected gitdir")
		}
	})

	t.Run("missing git dir refused", func(t *testing.T) {
		mainRoot := t.TempDir()
		worktreeDir := t.TempDir()
		gitDir := filepath.Join(mainRoot, ".git", "worktrees", "wt-gone")
		writeDotGit(t, worktreeDir, "gitdir: "+gitDir+"\n")
		if _, err := VerifyGitDir(context.Background(), mainRoot, "wt-gone", worktreeDir); err == nil {
			t.Fatal("VerifyGitDir accepts a gitdir that does not exist")
		}
	})
}

func TestVerifyGitDirRejectsSpoofedGitFile(t *testing.T) {
	mainRoot, name, worktreeDir := addWorktree(t)
	gitDir := filepath.Join(mainRoot, ".git", "worktrees", name)

	t.Run("trailing newline ok", func(t *testing.T) {
		writeDotGit(t, worktreeDir, "gitdir: "+gitDir+"\n")
		got, err := VerifyGitDir(context.Background(), mainRoot, name, worktreeDir)
		if err != nil {
			t.Fatalf("VerifyGitDir: %v", err)
		}
		if got != gitDir {
			t.Fatalf("git dir = %q, want %q", got, gitDir)
		}
	})

	t.Run("no trailing newline ok", func(t *testing.T) {
		writeDotGit(t, worktreeDir, "gitdir: "+gitDir)
		got, err := VerifyGitDir(context.Background(), mainRoot, name, worktreeDir)
		if err != nil {
			t.Fatalf("VerifyGitDir: %v", err)
		}
		if got != gitDir {
			t.Fatalf("git dir = %q, want %q", got, gitDir)
		}
	})

	t.Run("extra content refused", func(t *testing.T) {
		writeDotGit(t, worktreeDir, "gitdir: "+gitDir+"\nextra")
		if _, err := VerifyGitDir(context.Background(), mainRoot, name, worktreeDir); err == nil {
			t.Fatal("VerifyGitDir accepts extra content after the gitdir line")
		}
	})

	t.Run("bad prefix refused", func(t *testing.T) {
		writeDotGit(t, worktreeDir, "not a gitdir file\n")
		if _, err := VerifyGitDir(context.Background(), mainRoot, name, worktreeDir); err == nil {
			t.Fatal("VerifyGitDir accepts a .git file without the gitdir prefix")
		}
	})
}

func initRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	runGit(t, root, "init", "-b", "main")
	runGit(t, root, "config", "user.email", "test@example.com")
	runGit(t, root, "config", "user.name", "Test")
	// Pin line endings to LF: the delivery git context reads no system
	// config (GIT_CONFIG_NOSYSTEM=1), so a Windows autocrlf checkout would
	// otherwise produce CRLF working trees that look like diffs. The fixture
	// must be deterministic on every machine.
	runGit(t, root, "config", "core.autocrlf", "false")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "README.md")
	runGit(t, root, "commit", "-m", "init")
	return root
}

func addWorktree(t *testing.T) (mainRoot, name, worktreeDir string) {
	t.Helper()
	mainRoot = initRepo(t)
	worktreeDir = t.TempDir()
	name = filepath.Base(worktreeDir)
	runGit(t, mainRoot, "worktree", "add", "-b", "wt/"+name, worktreeDir)
	return mainRoot, name, worktreeDir
}

func writeDotGit(t *testing.T, worktreeDir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(worktreeDir, ".git"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}
