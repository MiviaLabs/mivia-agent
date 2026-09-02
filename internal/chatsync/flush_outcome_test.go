package chatsync

import (
	"errors"
	"fmt"
	"net"
	"testing"
)

type fakeNetErr struct{}

func (fakeNetErr) Error() string   { return "dial tcp: connection refused" }
func (fakeNetErr) Timeout() bool   { return false }
func (fakeNetErr) Temporary() bool { return true }

// TestClassifyFlushErrorTable is the one table every stop, retry and recover
// decision reads from. Flipping a row here is the sabotage the recovery
// tests exist to catch, so the table is pinned directly as well.
func TestClassifyFlushErrorTable(t *testing.T) {
	var _ net.Error = fakeNetErr{}
	cases := []struct {
		name string
		err  error
		want flushOutcome
	}{
		{"nil", nil, outcomeRetry},
		{"auth stop", fmt.Errorf("wrapped: %w", ErrAuthStop), outcomeStop},
		{"unauthorized before the auth policy resolves it", ErrUnauthorized, outcomeRetry},
		{"409 ended", &ConflictError{StatusCode: 409, Message: "session has ended"}, outcomeRecover},
		{"404 deleted", fmt.Errorf("append: %w", ErrNotFound), outcomeRecover},
		{"transcript conflict", ErrTranscriptConflict, outcomeRecover},
		{"400 sequence complaint keeps its rebase path", &BadRequestError{StatusCode: 400, Message: "sequence gap: expected 1, got 9"}, outcomeRetry},
		{"400 other is poison", &BadRequestError{StatusCode: 400, Message: "type must be at most 100 characters"}, outcomeStop},
		{"413 is poison", &BadRequestError{StatusCode: 413, Message: "payload too large"}, outcomeStop},
		{"422 is poison", &BadRequestError{StatusCode: 422, Message: "unprocessable"}, outcomeStop},
		{"bare ErrBadRequest is poison", ErrBadRequest, outcomeStop},
		{"5xx", errors.New("server error (500): boom"), outcomeRetry},
		{"net error", fakeNetErr{}, outcomeRetry},
		{"no token provider", ErrNoTokenProvider, outcomeRetry},
		{"invalid path id", ErrInvalidPathID, outcomeRecover},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyFlushError(tc.err); got != tc.want {
				t.Fatalf("classifyFlushError(%v) = %s, want %s", tc.err, got, tc.want)
			}
		})
	}
}
