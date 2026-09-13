package cliautomations

import (
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

func TestRunStateStringEveryCase(t *testing.T) {
	cases := []struct {
		state ports.RunState
		want  string
	}{
		{ports.RunPending, "pending"},
		{ports.RunRunning, "running"},
		{ports.RunSucceeded, "succeeded"},
		{ports.RunFailed, "failed"},
		{ports.RunCancelled, "cancelled"},
		{ports.RunInterrupted, "interrupted"},
		{ports.RunSkipped, "skipped"},
		{ports.RunState(99), "unknown"},
	}
	for _, c := range cases {
		if got := runStateString(c.state); got != c.want {
			t.Errorf("runStateString(%v) = %q, want %q", c.state, got, c.want)
		}
	}
}

func TestRunFailedOnlyTrueForFailed(t *testing.T) {
	cases := []struct {
		state ports.RunState
		want  bool
	}{
		{ports.RunFailed, true},
		{ports.RunSucceeded, false},
		{ports.RunSkipped, false},
		{ports.RunCancelled, false},
		{ports.RunPending, false},
	}
	for _, c := range cases {
		if got := runFailed(c.state); got != c.want {
			t.Errorf("runFailed(%v) = %v, want %v", c.state, got, c.want)
		}
	}
}

func TestPrintAutomationListEmpty(t *testing.T) {
	stdout, _ := captureOutput(t, func() { printAutomationList(nil) })
	if !strings.Contains(stdout, "no automations defined") {
		t.Fatalf("printAutomationList(nil) output = %q, want it to mention \"no automations defined\"", stdout)
	}
}

func TestPrintAutomationListNonEmpty(t *testing.T) {
	autos := []ports.Automation{
		{ID: "a1", Name: "First", Enabled: true},
		{ID: "a2", Name: "Second", Enabled: false},
	}
	stdout, _ := captureOutput(t, func() { printAutomationList(autos) })
	if !strings.Contains(stdout, "a1") || !strings.Contains(stdout, "enabled") {
		t.Fatalf("printAutomationList output = %q, want it to list the enabled automation", stdout)
	}
	if !strings.Contains(stdout, "a2") || !strings.Contains(stdout, "disabled") {
		t.Fatalf("printAutomationList output = %q, want it to list the disabled automation", stdout)
	}
}

func TestPrintAutomationDetailNoRunsNoDescription(t *testing.T) {
	a := ports.Automation{ID: "a1", Name: "First", Enabled: true}
	stdout, _ := captureOutput(t, func() { printAutomationDetail(a, nil) })
	if !strings.Contains(stdout, "(none)") {
		t.Fatalf("printAutomationDetail with no runs = %q, want it to mention \"(none)\"", stdout)
	}
	if strings.Contains(stdout, "description:") {
		t.Fatalf("printAutomationDetail with an empty description printed a description line: %q", stdout)
	}
}

func TestPrintAutomationDetailWithDescriptionAndRuns(t *testing.T) {
	a := ports.Automation{ID: "a1", Name: "First", Enabled: true, Description: "a test automation"}
	runs := []ports.Run{
		{ID: "run-1", State: ports.RunSucceeded, StartedAt: time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)},
		{ID: "run-2", State: ports.RunFailed, StartedAt: time.Date(2026, 9, 10, 9, 0, 0, 0, time.UTC)},
	}
	stdout, _ := captureOutput(t, func() { printAutomationDetail(a, runs) })
	if !strings.Contains(stdout, "description: a test automation") {
		t.Fatalf("printAutomationDetail output = %q, want the description line", stdout)
	}
	if !strings.Contains(stdout, "run-1") || !strings.Contains(stdout, "succeeded") {
		t.Fatalf("printAutomationDetail output = %q, want run-1's succeeded line", stdout)
	}
	if !strings.Contains(stdout, "run-2") || !strings.Contains(stdout, "failed") {
		t.Fatalf("printAutomationDetail output = %q, want run-2's failed line", stdout)
	}
}

func TestPrintRunResultNoMessage(t *testing.T) {
	r := ports.Run{ID: "run-1", State: ports.RunSucceeded}
	stdout, _ := captureOutput(t, func() { printRunResult(r) })
	if !strings.Contains(stdout, "run_id=run-1") || !strings.Contains(stdout, "state=succeeded") {
		t.Fatalf("printRunResult output = %q, want run_id and state", stdout)
	}
	if strings.Contains(stdout, "message:") {
		t.Fatalf("printRunResult with an empty message printed a message line: %q", stdout)
	}
}

func TestPrintRunResultWithMessage(t *testing.T) {
	r := ports.Run{ID: "run-1", State: ports.RunFailed, Message: "boom"}
	stdout, _ := captureOutput(t, func() { printRunResult(r) })
	if !strings.Contains(stdout, "message: boom") {
		t.Fatalf("printRunResult output = %q, want the message line", stdout)
	}
}
