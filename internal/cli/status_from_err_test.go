package cli

import (
	"context"
	"errors"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
)

// TestStatusFromErrWireBytes pins the model-facing status strings produced by
// statusFromErr (INV-AG-21). Values must stay byte-identical to ledger task statuses.
func TestStatusFromErrWireBytes(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, "failed"},
		{"deadline", context.DeadlineExceeded, "timed_out"},
		{"canceled", context.Canceled, "canceled"},
		{"deadline msg", errors.New("context deadline exceeded"), "timed_out"},
		{"canceled msg", errors.New("operation canceled"), "canceled"},
		{"cancelled msg", errors.New("operation cancelled"), "canceled"},
		{"other", errors.New("boom"), "failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := statusFromErr(tc.err)
			if got != tc.want {
				t.Fatalf("statusFromErr(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
	// Const wiring: returned strings equal ledger typed values.
	if statusFromErr(nil) != string(ledger.TaskStatusFailed) {
		t.Fatal("nil path must equal ledger.TaskStatusFailed")
	}
	if statusFromErr(context.DeadlineExceeded) != string(ledger.TaskStatusTimedOut) {
		t.Fatal("deadline path must equal ledger.TaskStatusTimedOut")
	}
	if statusFromErr(context.Canceled) != string(ledger.TaskStatusCanceled) {
		t.Fatal("canceled path must equal ledger.TaskStatusCanceled")
	}
}
