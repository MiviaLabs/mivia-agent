package ledger

import (
	"context"
	"fmt"
	"sync"
)

// PanelContentValidator validates the referenced content of one durable
// panel task at attempt admission: digests, schemas and the coordinator
// request fingerprint. The implementation lives in internal/workflows/panel
// (ValidateTaskContent); it needs the coordinator and subagent runtime, and
// this persistence package must not link those. Every production binary
// registers it via the panel package's init; a repository without the
// validator FAILS CLOSED and refuses every panel attempt.
type PanelContentValidator func(ctx context.Context, repo Repository, workflowRunID, taskID string, work PanelTaskSpec) error

var (
	panelContentValidatorMu sync.RWMutex
	panelContentValidator   PanelContentValidator
)

// SetPanelContentValidator installs the panel content validator. It is
// called by the panel package's init; callers must not pass nil.
func SetPanelContentValidator(v PanelContentValidator) {
	panelContentValidatorMu.Lock()
	defer panelContentValidatorMu.Unlock()
	panelContentValidator = v
}

func loadPanelContentValidator() PanelContentValidator {
	panelContentValidatorMu.RLock()
	defer panelContentValidatorMu.RUnlock()
	return panelContentValidator
}

// validatePanelTaskContent runs the registered content validator for one
// panel task (attempt admission and synthesis admission). Without a
// registered validator it fails closed.
func (s *StorageRepository) validatePanelTaskContent(ctx context.Context, workflowRunID, taskID string, work PanelTaskSpec) error {
	validator := loadPanelContentValidator()
	if validator == nil {
		return fmt.Errorf("panel task %s refused: no panel content validator registered (link internal/workflows/panel)", taskID)
	}
	return validator(ctx, s, workflowRunID, taskID, work)
}

// validateInitialPanelAttempt runs the structural panel checks and then the
// registered content validator for every member. Without a registered
// validator it refuses the attempt: unverified panel content must never be
// persisted.
func (s *StorageRepository) validateInitialPanelAttempt(ctx context.Context, attempt StepAttempt) error {
	if err := attempt.PanelExecution.validateInitial(attempt.RunID, attempt.AttemptID); err != nil {
		return err
	}
	if attempt.PanelExecution == nil {
		return nil
	}
	validator := loadPanelContentValidator()
	if validator == nil {
		return fmt.Errorf("panel attempt %s refused: no panel content validator registered (link internal/workflows/panel)", attempt.AttemptID)
	}
	for _, member := range attempt.PanelExecution.Members {
		if err := validator(ctx, s, attempt.RunID, member.TaskID, member.Work); err != nil {
			return err
		}
	}
	return nil
}
