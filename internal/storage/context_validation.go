package storage

import (
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

func validateEnsureRequest(request contextstate.EnsureSessionRequest) error {
	if err := request.Principal.Validate(); err != nil {
		return err
	}
	if !request.Principal.IsBound() {
		return fmt.Errorf("%w: owner capability is not bound", contextstate.ErrPrincipalMismatch)
	}
	if !contextstate.ValidSessionDir(request.Dir) || !contextstate.ValidSessionDir(request.Worktree) {
		return fmt.Errorf("%w: invalid session directory metadata", contextstate.ErrInvalidDTO)
	}
	if err := request.WorktreeInstance.Validate(); err != nil {
		return err
	}
	return request.Binding.Validate()
}
