package storage

import "fmt"

// Failure-injection seam for the durable context commit path: a test arms one
// named step, the next call that reaches that step fails once, and the arm
// clears itself. See sqlite_failure_test.go for the tests that drive it.
const (
	contextFailureAfterSessionCreation = "after-session-creation"
	contextFailurePayloadInsert        = "payload-insert"
	contextFailureSourceAppend         = "source-append"
	contextFailureCheckpointInsert     = "checkpoint-insert"
	contextFailureCompletionMark       = "completion-mark"
	contextFailureActivePointerUpdate  = "active-pointer-update"
	contextFailureRevisionUpdate       = "revision-update"
)

func (s *SQLite) contextFailure(step string) error {
	s.failureMu.Lock()
	defer s.failureMu.Unlock()
	if s.contextFailureStep != step {
		return nil
	}
	s.contextFailureStep = ""
	return fmt.Errorf("injected context failure at %s", step)
}

func (s *SQLite) injectContextFailure(step string) {
	s.failureMu.Lock()
	s.contextFailureStep = step
	s.failureMu.Unlock()
}
