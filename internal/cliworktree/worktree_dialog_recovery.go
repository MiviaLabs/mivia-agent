package cliworktree

import (
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// CanonicalWorktreeDialogRoot is the canonical worktree dialog root value.
var CanonicalWorktreeDialogRoot = CanonicalMarkerRoot

// WorktreeRecoveryRow holds worktree recovery row state.
type WorktreeRecoveryRow struct {
	Info contextstate.WorktreeInstanceInfo
}

// WorktreeDialogBinding holds worktree dialog binding state.
type WorktreeDialogBinding struct {
	Instance contextstate.WorktreeInstance
	Err      error
}

// WorktreeRecoveryLabel implements worktree recovery label.
func WorktreeRecoveryLabel(state contextstate.WorktreeInstanceState) string {
	if state == contextstate.WorktreeCreating {
		return "creation recovery required"
	}
	return "recovery required"
}
