package contextstate

import "strings"

// WorktreeInstance identifies one lifetime of a managed worktree.
// The random ID prevents an old process from using a same-name replacement.
type WorktreeInstance struct {
	Worktree string `json:"worktree"`
	ID       string `json:"id"`
}

// WorktreeInstanceState is the durable lifecycle state of one instance.
type WorktreeInstanceState string

const (
	WorktreeCreating WorktreeInstanceState = "creating"
	WorktreeActive   WorktreeInstanceState = "active"
	WorktreeDeleting WorktreeInstanceState = "deleting"
	WorktreeDeleted  WorktreeInstanceState = "deleted"
)

// WorktreeInstanceInfo is the catalog record for one physical worktree.
type WorktreeInstanceInfo struct {
	Instance      WorktreeInstance
	CanonicalPath string
	State         WorktreeInstanceState
}

// IsZero reports whether no managed worktree binding exists.
func (i WorktreeInstance) IsZero() bool {
	return i.Worktree == "" && i.ID == ""
}

// Validate rejects a partial or unsafe worktree binding.
func (i WorktreeInstance) Validate() error {
	if i.IsZero() {
		return nil
	}
	if err := validateIdentifier("worktree_instance.worktree", i.Worktree); err != nil {
		return err
	}
	if strings.ContainsAny(i.Worktree, `/\\`) {
		return invalid("worktree_instance.worktree", "contains a path separator")
	}
	return validateIdentifier("worktree_instance.id", i.ID)
}
