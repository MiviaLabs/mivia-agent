package cliworktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

func markerGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	commandArgs := append([]string{"-C", dir}, args...)
	output, err := exec.Command("git", commandArgs...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func markerLinkedWorktree(t *testing.T) (string, string) {
	t.Helper()
	repository := t.TempDir()
	markerGitOutput(t, repository, "init", "-q")
	markerGitOutput(t, repository, "config", "user.email", "test@example.invalid")
	markerGitOutput(t, repository, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repository, "seed.txt"), []byte("seed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	markerGitOutput(t, repository, "add", "seed.txt")
	markerGitOutput(t, repository, "commit", "-q", "-m", "test: seed repository")
	linked := filepath.Join(t.TempDir(), "linked")
	markerGitOutput(t, repository, "worktree", "add", "-q", "-b", "wt-a", linked)
	return repository, linked
}

func markerCommonExcludePath(t *testing.T, linked string) string {
	t.Helper()
	commonDir := markerGitOutput(t, linked, "rev-parse", "--git-common-dir")
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(linked, commonDir)
	}
	return filepath.Clean(filepath.Join(commonDir, "info", "exclude"))
}

func TestWorktreeMarkerRoundTripAndRejectsWrongRoot(t *testing.T) {
	root := t.TempDir()
	markerGitOutput(t, root, "init", "-q")
	instance := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1234567890abcdef"}
	if err := WriteWorktreeMarker(root, instance); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	got, err := ReadWorktreeMarker(root)
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if got != instance {
		t.Fatalf("marker = %+v, want %+v", got, instance)
	}
	if err := os.Mkdir(filepath.Join(root, "child"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadWorktreeMarker(filepath.Join(root, "child")); err == nil {
		t.Fatal("subdirectory marker read succeeded")
	}
}

func TestWorktreeMarkerRejectsSymlinkDirectory(t *testing.T) {
	root := t.TempDir()
	markerGitOutput(t, root, "init", "-q")
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, ".mivia")); err != nil {
		t.Fatal(err)
	}
	instance := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1234567890abcdef"}
	if err := WriteWorktreeMarker(root, instance); err == nil {
		t.Fatal("write through symlink directory succeeded")
	}
}

func TestWorktreeMarkerRejectsSymlinkGitInfo(t *testing.T) {
	root := t.TempDir()
	markerGitOutput(t, root, "init", "-q")
	infoDir := filepath.Join(root, ".git", "info")
	if err := os.RemoveAll(infoDir); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	excludePath := filepath.Join(outside, "exclude")
	initial := []byte("# outside content\n")
	if err := os.WriteFile(excludePath, initial, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, infoDir); err != nil {
		t.Fatal(err)
	}

	instance := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1234567890abcdef"}
	if err := WriteWorktreeMarker(root, instance); err == nil {
		t.Errorf("write with symlinked Git info directory succeeds")
	}
	content, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != string(initial) {
		t.Errorf("outside exclude content = %q, want %q", content, initial)
	}
	if _, err := os.Lstat(WorktreeMarkerPath(root)); !os.IsNotExist(err) {
		t.Errorf("marker publication error = %v, want not exist", err)
	}
}

func TestWorktreeMarkerIsExcludedFromLinkedWorktree(t *testing.T) {
	_, linked := markerLinkedWorktree(t)
	instance := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1234567890abcdef"}
	if err := WriteWorktreeMarker(linked, instance); err != nil {
		t.Fatal(err)
	}
	markerPath := filepath.Join(".mivia", worktreeMarkerName)
	command := exec.Command("git", "-C", linked, "check-ignore", "--quiet", markerPath)
	if output, err := command.CombinedOutput(); err != nil {
		t.Errorf("git check-ignore did not match marker: %v\n%s", err, output)
	}
	markerGitOutput(t, linked, "add", ".")
	staged := markerGitOutput(t, linked, "diff", "--cached", "--name-only")
	if strings.Contains(staged, markerPath) {
		t.Errorf("git add staged marker: %q", staged)
	}
}

func TestWorktreeMarkerExcludePreservesContentAndIsIdempotent(t *testing.T) {
	_, linked := markerLinkedWorktree(t)
	excludePath := markerCommonExcludePath(t, linked)
	initial := "# keep this entry\nlocal.cache\n"
	if err := os.WriteFile(excludePath, []byte(initial), 0600); err != nil {
		t.Fatal(err)
	}
	instance := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1234567890abcdef"}
	for range 2 {
		if err := WriteWorktreeMarker(linked, instance); err != nil {
			t.Fatal(err)
		}
	}
	content, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), initial) {
		t.Errorf("common exclude content was not preserved: %q", content)
	}
	if count := strings.Count(string(content), worktreeMarkerName); count != 1 {
		t.Errorf("marker exclude count = %d, want 1; content = %q", count, content)
	}
}

func TestWorktreeMarkerExcludeIsConcurrentAndIdempotent(t *testing.T) {
	_, linked := markerLinkedWorktree(t)
	excludePath := markerCommonExcludePath(t, linked)
	instance := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1234567890abcdef"}
	errorsCh := make(chan error, 8)
	var group sync.WaitGroup
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			errorsCh <- WriteWorktreeMarker(linked, instance)
		}()
	}
	group.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatalf("concurrent marker write: %v", err)
		}
	}
	content, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(content), worktreeMarkerName); count != 1 {
		t.Errorf("concurrent marker exclude count = %d, want 1; content = %q", count, content)
	}
}

func TestWorktreeMarkerRecoversFromUnownedExcludeLock(t *testing.T) {
	_, linked := markerLinkedWorktree(t)
	excludePath := markerCommonExcludePath(t, linked)
	if err := os.WriteFile(excludePath+".lock", nil, 0600); err != nil {
		t.Fatal(err)
	}
	instance := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1234567890abcdef"}
	firstErr := WriteWorktreeMarker(linked, instance)
	secondErr := WriteWorktreeMarker(linked, instance)
	if firstErr != nil || secondErr != nil {
		t.Fatalf("marker writes with unowned exclude lock = %v, %v; want nil", firstErr, secondErr)
	}
	content, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(content), worktreeMarkerName); count != 1 {
		t.Fatalf("marker exclude count = %d, want 1; content = %q", count, content)
	}
}
