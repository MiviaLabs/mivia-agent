package subagents

// "timed_out" is a claim about the run's own budget. A transport stage timeout
// (net/http's response-header bound, the client-wide backstop) reports
// errors.Is(err, context.DeadlineExceeded) without any budget being spent, so
// classifying it as "timed_out" tells the operator a deadline they configured
// fired when none did - and sends them tuning timeouts that were never the
// cause.

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// stageTimeoutErr mimics net/http's unexported timeoutError: it claims
// equality with context.DeadlineExceeded without carrying it.
type stageTimeoutErr struct{}

func (stageTimeoutErr) Error() string { return "net/http: timeout awaiting response headers" }
func (stageTimeoutErr) Is(err error) bool {
	return err == context.DeadlineExceeded
}
func (stageTimeoutErr) Timeout() bool   { return true }
func (stageTimeoutErr) Temporary() bool { return true }

func TestTerminalStatusSeparatesStageTimeoutFromBudget(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"transport stage timeout is a failure, not a budget", stageTimeoutErr{}, "error"},
		{"wrapped stage timeout", fmt.Errorf("llmproxycli: %w", stageTimeoutErr{}), "error"},
		{"a real request budget still times out", context.DeadlineExceeded, "timed_out"},
		{"a wrapped real budget still times out", fmt.Errorf("request budget: %w", context.DeadlineExceeded), "timed_out"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := terminalStatus(tc.err); got != tc.want {
				t.Fatalf("terminalStatus(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

// The envelope the parent reads must agree with the done event: a stage
// timeout is an error, so its output is dropped rather than presented as a
// partial answer from a run that merely ran out of clock.
func TestBuildResultStageTimeoutIsError(t *testing.T) {
	payload, err := buildResult("half an answer", 4, 0, 2, stageTimeoutErr{})
	if err == nil {
		t.Fatal("expected the run error to propagate")
	}
	body := string(payload)
	if !strings.Contains(body, `"status":"error"`) {
		t.Fatalf("envelope must report error status, got %s", body)
	}
	if strings.Contains(body, "half an answer") {
		t.Fatalf("a failed run must not present its partial output, got %s", body)
	}
}
