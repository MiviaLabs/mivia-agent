package cli

import (
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

var canonicalWorktreeDialogRoot = canonicalMarkerRoot

type worktreeRecoveryRow struct {
	Info contextstate.WorktreeInstanceInfo
}

type worktreeDialogBinding struct {
	Instance contextstate.WorktreeInstance
	Err      error
}

func worktreeRecoveryLabel(state contextstate.WorktreeInstanceState) string {
	if state == contextstate.WorktreeCreating {
		return "creation recovery required"
	}
	return "recovery required"
}
