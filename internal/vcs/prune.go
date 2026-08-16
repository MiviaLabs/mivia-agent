package vcs

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// maxGitdirFileSize bounds the .git/worktrees/<name>/gitdir pointer read so a
// corrupt or hostile pointer file cannot exhaust memory.
const maxGitdirFileSize = 1 << 20 // 1 MiB

// pruneStaleWorktree removes the single git worktree registration for name
// when — and only when — the registration is unlocked, its gitdir pointer is
// readable, and the recorded working-tree directory no longer exists. It is
// the targeted replacement for `git worktree prune`, which drops
// registrations of live worktrees whose on-disk .git gitfile is missing
// (git's should_prune_worktree treats a missing gitfile as stale because the
// admin index-mtime fallback is always defeated at prune's expire=TIME_MAX),
// silently breaking List/Resolve discoverability. When any precondition
// fails the helper returns nil and preserves the registration: it fails
// closed toward discoverability and never removes an intact working tree.
func pruneStaleWorktree(ctx context.Context, root, name string) error {
	root, _ = filepath.Abs(root) // Abs only fails if Getwd fails; callers validate the root first
	sanitised, err := SanitizeName(name)
	if err != nil {
		return err
	}
	adminDir := filepath.Join(resolveGitDir(filepath.Join(root, ".git")), "worktrees", sanitised)
	// A locked registration is never pruned (git's own contract).
	if _, err := os.Stat(filepath.Join(adminDir, "locked")); err == nil {
		return nil
	}
	wtPath, err := workingTreePath(adminDir)
	if err != nil {
		return nil // pointer unreadable or oversized: preserve the registration
	}
	if wtPath == "" {
		return nil // pointer unparseable: preserve the registration
	}
	if _, err := os.Stat(wtPath); err == nil {
		return nil // the working tree still exists: never drop it
	} else if !os.IsNotExist(err) {
		return nil // stat failed for another reason: fail closed and preserve
	}
	// The recorded working-tree directory is confirmed gone. Remove only the
	// registration directory (adminDir is derived from a sanitised name under
	// the repo's resolved .git/worktrees/), never the working tree itself.
	return os.RemoveAll(adminDir)
}

// workingTreePath reads .git/worktrees/<name>/gitdir (the admin pointer git
// keeps for a linked worktree) through a bounded reader and derives the
// working-tree root it records. A missing, unreadable, or oversized pointer
// file returns an error; an unparseable pointer returns "".
func workingTreePath(adminDir string) (string, error) {
	f, err := os.Open(filepath.Join(adminDir, "gitdir"))
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxGitdirFileSize+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxGitdirFileSize {
		return "", errors.New("worktree gitdir pointer exceeds maxGitdirFileSize")
	}
	return worktreePathFromGitdir(data, adminDir), nil
}

// worktreePathFromGitdir derives the recorded working-tree root from a
// .git/worktrees/<name>/gitdir pointer file. Git writes the path to the
// worktree's .git file (ending in .git); stripping that element yields the
// working tree, mirroring git's get_linked_worktree. A relative pointer is
// anchored to the admin worktree directory, matching git's own anchoring.
// Any input that cannot be parsed into a .git-suffixed path yields "" so
// callers preserve the registration (fail closed). Pure and panic-free for
// arbitrary input; this is the surface the fuzz target drives.
func worktreePathFromGitdir(data []byte, adminDir string) string {
	trimmed := strings.TrimRight(string(data), "\r\n")
	if trimmed == "" {
		return ""
	}
	line := trimmed
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return ""
	}
	// A gitfile-formatted line ("gitdir: <path>") is the format of the
	// worktree's own .git file, not of the admin pointer this parser reads:
	// .git/worktrees/<name>/gitdir holds a bare path (git's
	// get_linked_worktree). Refuse to guess a working tree from it and fail
	// closed, preserving the registration.
	if strings.HasPrefix(line, "gitdir:") {
		return ""
	}
	line = filepath.FromSlash(line)
	if !filepath.IsAbs(line) {
		line = filepath.Join(adminDir, line)
	}
	line = filepath.Clean(line)
	if filepath.Base(line) != ".git" {
		return "" // not a pointer to a worktree .git file; refuse to guess
	}
	return filepath.Dir(line)
}
