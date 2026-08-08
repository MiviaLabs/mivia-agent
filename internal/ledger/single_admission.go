package ledger

func validateSingleTaskAdmission(a SingleTaskAdmission) error {
	if a.IdempotencyKey == "" || a.Run.RunID == "" || a.Task.RunID != a.Run.RunID || a.Task.TaskID == "" || len(a.Task.Attempts) != 1 {
		return ErrInvalidTransition
	}
	attempt := a.Task.Attempts[0]
	if attempt.RunID != a.Run.RunID || attempt.TaskID != a.Task.TaskID || attempt.AttemptID == "" || attempt.AttemptNum != 1 || attempt.Status != a.Task.Status {
		return ErrInvalidTransition
	}
	if a.Run.Status == RunStatusCanceled {
		if a.Task.Status != string(TaskStatusCanceled) || attempt.Status != string(TaskStatusCanceled) || a.Run.CompletedAt == nil || a.Task.CompletedAt == nil || attempt.FinishedAt == nil {
			return ErrInvalidTransition
		}
		return nil
	}
	if a.Run.Status != RunStatusCreated || a.Task.Status != string(TaskStatusQueued) || attempt.Status != string(TaskStatusQueued) {
		return ErrInvalidTransition
	}
	return nil
}
