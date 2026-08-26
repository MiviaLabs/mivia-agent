package welcome

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/vcs"
)

// detectWorkspace walks up from dir looking for a .git entry, resolves
// gitdir: indirection (reusing internal/vcs's existing no-exec resolver
// per the amendment), and reads the resulting HEAD. ok is false when no
// repo is found or HEAD cannot be parsed. No git executable is invoked.
func detectWorkspace(dir string) (repoName, branch string, ok bool) {
	root, gitDir, found := findGitDir(dir)
	if !found {
		return "", "", false
	}
	data, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return "", "", false
	}
	branch, ok = parseHEAD(data)
	if !ok {
		return "", "", false
	}
	return filepath.Base(root), branch, true
}

// findGitDir walks up from dir via filepath.Dir looking for a .git entry,
// which may be a directory (the ordinary case) or a gitdir-pointer file
// (linked worktrees). The walk terminates when filepath.Dir stops making
// progress (the filesystem root), so an isolated tree with no repo returns
// found=false rather than looping. root is the directory that contained
// the .git entry; gitDir is the resolved git directory to read HEAD from.
func findGitDir(dir string) (root, gitDir string, found bool) {
	cur := dir
	for {
		candidate := filepath.Join(cur, ".git")
		if _, err := os.Stat(candidate); err == nil {
			return cur, vcs.ResolveGitDir(candidate), true
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", "", false
		}
		cur = parent
	}
}

// parseHEAD extracts a branch name from HEAD file contents ("ref:
// refs/heads/<name>\n"), or a 7-char short SHA for detached HEAD. ok is
// false for empty/unparseable content.
func parseHEAD(data []byte) (branch string, ok bool) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return "", false
	}

	if rest, isRef := strings.CutPrefix(trimmed, "ref: "); isRef {
		ref := strings.TrimSpace(rest)
		const prefix = "refs/heads/"
		name, isBranch := strings.CutPrefix(ref, prefix)
		if !isBranch || name == "" {
			return "", false
		}
		return name, true
	}

	// Detached HEAD: expect a hex commit SHA, short it to 7 characters.
	if !isHexSHA(trimmed) {
		return "", false
	}
	return trimmed[:7], true
}

// isHexSHA reports whether s looks like a plausible git object id: at
// least 7 lowercase hex characters (short SHAs are never shorter).
func isHexSHA(s string) bool {
	if len(s) < 7 {
		return false
	}
	for _, r := range s {
		isHex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')
		if !isHex {
			return false
		}
	}
	return true
}
