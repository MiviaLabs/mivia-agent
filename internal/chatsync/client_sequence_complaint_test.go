package chatsync

import "testing"

// TestIsSequenceComplaintMatchesBothRecoverableShapes pins the classifier that
// routes a 400 to the rebase path. The server names a sequence problem in two
// wordings - the gap shape ("sequence gap: ...") and the in-batch shape
// ("Non-contiguous batch: events[1] has seq 2494, expected 2493") - and both
// heal by rebasing on the server mark. A schema-validation message names no
// sequence problem and must stay poison.
func TestIsSequenceComplaintMatchesBothRecoverableShapes(t *testing.T) {
	recoverable := []string{
		"Non-contiguous batch: events[1] has seq 2494, expected 2493",
		"non-contiguous batch: events[0] has seq 5, expected 1",
		"NON-CONTIGUOUS BATCH: events[2] has seq 9, expected 7",
		"sequence gap: expected 1, got 9",
		"Sequence is not contiguous within the batch",
	}
	for _, msg := range recoverable {
		err := &BadRequestError{StatusCode: 400, Message: msg}
		if !err.IsSequenceComplaint() {
			t.Errorf("IsSequenceComplaint() = false for %q, want true", msg)
		}
	}

	poison := []string{
		"type must be at most 100 characters",
		"events must contain no more than 100 elements",
		"",
	}
	for _, msg := range poison {
		err := &BadRequestError{StatusCode: 400, Message: msg}
		if err.IsSequenceComplaint() {
			t.Errorf("IsSequenceComplaint() = true for %q, want false", msg)
		}
	}
}
