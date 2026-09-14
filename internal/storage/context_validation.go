package storage

import (
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/context/state"
)

func validateEnsureRequest(request state.EnsureSessionRequest) error {
	if err := request.Principal.Validate(); err != nil {
		return err
	}
	if !request.Principal.IsBound() {
		return fmt.Errorf("%w: owner capability is not bound", state.ErrPrincipalMismatch)
	}
	if !state.ValidSessionDir(request.Dir) || !state.ValidSessionDir(request.Worktree) {
		return fmt.Errorf("%w: invalid session directory metadata", state.ErrInvalidDTO)
	}
	if err := request.WorktreeInstance.Validate(); err != nil {
		return err
	}
	return request.Binding.Validate()
}
