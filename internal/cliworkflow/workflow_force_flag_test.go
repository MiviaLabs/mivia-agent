package cliworkflow

// Regression tests for the --force flag (FIX: silent flag).
//
// --force used to be stripped from the argument list for every subcommand,
// but only deliver/resume/delete consume it; `workflow run --force x`,
// `workflow cancel --force <id>`, etc. silently ignored it. Subcommands that
// do not accept --force must now reject it with an explicit error.

import (
	"io"
	"strings"
	"testing"
)

// TestWorkflowForceRejectedForSubcommandsWithoutForce: --force is only a flag
// of deliver/resume/delete; every other subcommand must refuse it loudly
// instead of silently stripping it and proceeding.
func TestWorkflowForceRejectedForSubcommandsWithoutForce(t *testing.T) {
	for _, sub := range []string{"run", "runs", "status", "events", "approve", "reject", "cancel", "cleanup"} {
		t.Run(sub, func(t *testing.T) {
			err := RunWorkflowWithIO([]string{sub, "--force"}, io.Discard, io.Discard)
			if err == nil {
				t.Fatalf("workflow %s --force: expected an error rejecting --force", sub)
			}
			if !strings.Contains(err.Error(), "--force") {
				t.Fatalf("workflow %s --force error = %q, want it to reject --force", sub, err.Error())
			}
		})
	}
}

// TestWorkflowForceStillAcceptedForDeliverResumeDelete pins the accepted set:
// deliver/resume/delete continue to parse --force (they fail on their own
// input validation, NOT on a --force rejection).
func TestWorkflowForceStillAcceptedForDeliverResumeDelete(t *testing.T) {
	for _, sub := range []string{"deliver", "resume", "delete"} {
		t.Run(sub, func(t *testing.T) {
			err := RunWorkflowWithIO([]string{sub, "--force", "--workspace", t.TempDir()}, io.Discard, io.Discard)
			if err == nil {
				t.Fatalf("workflow %s --force: expected an input-validation error, not success", sub)
			}
			if strings.Contains(err.Error(), "--force is not supported") {
				t.Fatalf("workflow %s --force was rejected: %v", sub, err)
			}
		})
	}
}
