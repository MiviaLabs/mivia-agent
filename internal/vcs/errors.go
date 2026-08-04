package vcs

import "fmt"

// NotGitRepoError indicates the directory is not inside a git repository
// or git is not available.
type NotGitRepoError struct {
	Dir string
}

func (e NotGitRepoError) Error() string {
	return fmt.Sprintf("not a git repository: %s", e.Dir)
}

// WorktreeExistsError indicates a worktree with the given name already exists.
type WorktreeExistsError struct {
	Name string
}

func (e WorktreeExistsError) Error() string {
	return fmt.Sprintf("worktree %q already exists", e.Name)
}

// WorktreeNotFoundError indicates no worktree with the given name exists.
type WorktreeNotFoundError struct {
	Name string
}

func (e WorktreeNotFoundError) Error() string {
	return fmt.Sprintf("worktree %q not found", e.Name)
}
