package legacytui

import (
	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// worktreeDialog-receiver methods: recovery-row bookkeeping on the dialog's
// own state, as opposed to the *TUIModel methods in
// worktree_dialog_recovery_methods.go that validate and recover worktree
// instances against the lifecycle store.

func (d *worktreeDialog) setRecovery(info contextstate.WorktreeInstanceInfo) {
	d.recovery[info.Instance.Worktree] = cli.WorktreeRecoveryRow{Info: info}
}

func (d *worktreeDialog) selectedRecovery() (cli.WorktreeRecoveryRow, bool) {
	worktree, ok := d.selected()
	if !ok {
		return cli.WorktreeRecoveryRow{}, false
	}
	recovery, ok := d.recovery[worktree.Name]
	return recovery, ok
}
