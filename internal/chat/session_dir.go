package chat

import (
	"context"
	"os"

	"github.com/MiviaLabs/mivia-agent/internal/vcs"
)

// currentDirContext captures the directory and mivia worktree a session
// lives in at save/commit time. The TUI restores this directory when the
// session is reopened, so a session created inside a worktree comes back
// inside that worktree.
//
// The worktree probe is best-effort: a session saved outside any git repo
// must still save, so the vcs error is swallowed and worktree stays empty.
// The name is the mivia worktree's base name; a session saved from a
// subdirectory of a worktree reports the subdirectory (restore still lands
// on the exact saved directory).
func currentDirContext() (dir, worktree string) {
	dir, err := os.Getwd()
	if err != nil {
		return "", ""
	}
	if _, err := os.Stat(dir); err != nil {
		return "", ""
	}
	wt, _ := vcs.CurrentWorktreeName(context.Background(), dir)
	return dir, wt
}
