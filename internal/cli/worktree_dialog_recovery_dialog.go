package cli

import (
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// worktreeDialog-receiver methods: recovery-row bookkeeping on the dialog's
// own state, as opposed to the *tuiModel methods in
// worktree_dialog_recovery_methods.go that validate and recover worktree
// instances against the lifecycle store.

func (d *worktreeDialog) setRecovery(info contextstate.WorktreeInstanceInfo) {
	d.recovery[info.Instance.Worktree] = worktreeRecoveryRow{Info: info}
}

func (d *worktreeDialog) selectedRecovery() (worktreeRecoveryRow, bool) {
	worktree, ok := d.selected()
	if !ok {
		return worktreeRecoveryRow{}, false
	}
	recovery, ok := d.recovery[worktree.Name]
	return recovery, ok
}
