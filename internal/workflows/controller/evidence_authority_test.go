package controller

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/evidencecheck"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// passReport claims a PASS for `make verify`. Claims only parse inside an
// Evidence section, so the header is load-bearing: with any other header the
// report yields zero claims and every assertion below would pass vacuously.
const passReport = "mivia-report/v1\nEvidence:\n- make verify: PASS\n"

// TestPassReportActuallyParsesAClaim guards the fixture itself.
func TestPassReportActuallyParsesAClaim(t *testing.T) {
	if claims := evidencecheck.ParseClaims(passReport); len(claims) != 1 || claims[0].ClaimedVerdict != "PASS" {
		t.Fatalf("ParseClaims(passReport) = %+v, want exactly one PASS claim", claims)
	}
}

// TestEvidenceHistoryComesFromRecordedRunCommands proves the gate now reads the
// host's own trace: a recorded, completed run_command with exit=0 verifies the
// matching PASS claim.
func TestEvidenceHistoryComesFromRecordedRunCommands(t *testing.T) {
	steps := []subagents.ToolCallStep{
		{ToolCallID: "c1", Name: "run_command", Kind: "start", Input: `{"argv":["make","verify"]}`},
		{ToolCallID: "c1", Name: "run_command", Kind: "end", Output: "exit=0\nall gates passed\n"},
	}
	history := toolExecutionHistory(steps)
	if len(history) != 1 {
		t.Fatalf("history = %+v, want exactly one record", history)
	}
	if got := history[0].ExitCode; got != 0 {
		t.Fatalf("ExitCode = %d, want 0", got)
	}
	if err := ValidateReportEvidence(passReport, history); err != nil {
		t.Fatalf("ValidateReportEvidence on a truthful PASS = %v, want nil", err)
	}
}

// TestRecordedFailureRefusesAPassClaim is the other half: the trace, not the
// report, decides. A command that really ran and really failed cannot be
// reported as PASS.
func TestRecordedFailureRefusesAPassClaim(t *testing.T) {
	steps := []subagents.ToolCallStep{
		{ToolCallID: "c1", Name: "run_command", Kind: "start", Input: `{"argv":["make","verify"]}`},
		{ToolCallID: "c1", Name: "run_command", Kind: "end", Output: "FAIL internal/x\nexit=2\n"},
	}
	history := toolExecutionHistory(steps)
	if len(history) != 1 || history[0].ExitCode != 2 {
		t.Fatalf("history = %+v, want one record with exit 2", history)
	}
	if err := ValidateReportEvidence(passReport, history); err == nil {
		t.Fatal("ValidateReportEvidence accepted a PASS claim for a command that exited 2")
	}
}

// TestUnfinishedCommandProvesNothing pins the start-without-end case: a
// command that was launched but never completed is not evidence it passed.
func TestUnfinishedCommandProvesNothing(t *testing.T) {
	steps := []subagents.ToolCallStep{
		{ToolCallID: "c1", Name: "run_command", Kind: "start", Input: `{"argv":["make","verify"]}`},
	}
	if history := toolExecutionHistory(steps); len(history) != 0 {
		t.Fatalf("history = %+v, want none for a command with no recorded end", history)
	}
}

// TestUnreadableExitStatusIsNotASuccess covers run_command's non-numeric
// statuses (exit=timeout, exit=canceled, exit=error) and a missing header.
// None of them may read as a clean zero.
func TestUnreadableExitStatusIsNotASuccess(t *testing.T) {
	for _, output := range []string{"exit=timeout\n", "exit=canceled\n", "exit=error\n", "no status header at all\n"} {
		steps := []subagents.ToolCallStep{
			{ToolCallID: "c1", Name: "run_command", Kind: "start", Input: `{"argv":["make","verify"]}`},
			{ToolCallID: "c1", Name: "run_command", Kind: "end", Output: output},
		}
		history := toolExecutionHistory(steps)
		if len(history) != 1 {
			t.Fatalf("output %q: history = %+v, want one record", output, history)
		}
		if history[0].ExitCode == 0 {
			t.Errorf("output %q read as exit 0; an unreadable status must not pass a claim", output)
		}
		if err := ValidateReportEvidence(passReport, history); err == nil {
			t.Errorf("output %q: a PASS claim was accepted on an unreadable exit status", output)
		}
	}
}

// TestNonRunCommandStepsAreIgnored keeps unrelated tools out of the execution
// history, so a read_file call cannot stand in for a verification command.
func TestNonRunCommandStepsAreIgnored(t *testing.T) {
	steps := []subagents.ToolCallStep{
		{ToolCallID: "c1", Name: "read_file", Kind: "start", Input: `{"argv":["make","verify"]}`},
		{ToolCallID: "c1", Name: "read_file", Kind: "end", Output: "exit=0\n"},
	}
	if history := toolExecutionHistory(steps); len(history) != 0 {
		t.Fatalf("history = %+v, want none: only run_command executions are evidence", history)
	}
}

// TestEmptyTraceFailsClosed states the whole contract in one line: with no
// recorded executions, a PASS claim is refused rather than trusted.
func TestEmptyTraceFailsClosed(t *testing.T) {
	if err := ValidateReportEvidence(passReport, nil); err == nil {
		t.Fatal("a PASS claim was accepted with no recorded tool executions")
	}
	var empty []evidencecheck.ToolExecutionRecord
	if err := ValidateReportEvidence(passReport, empty); err == nil {
		t.Fatal("a PASS claim was accepted against an empty history")
	}
}

// TestStartWithNoArgvIsNotAnExecution pins the second arm of the start guard:
// well-formed JSON carrying an EMPTY argv names no command, so it cannot open
// a pending execution. A mutant turning the "||" into "&&" would accept it and
// mint a record whose command line is empty - which matches nothing, but does
// so by accident rather than by rule.
func TestStartWithNoArgvIsNotAnExecution(t *testing.T) {
	for _, input := range []string{`{"argv":[]}`, `{}`, `{"argv":null}`, `not json at all`} {
		steps := []subagents.ToolCallStep{
			{ToolCallID: "c1", Name: "run_command", Kind: "start", Input: input},
			{ToolCallID: "c1", Name: "run_command", Kind: "end", Output: "exit=0\n"},
		}
		if history := toolExecutionHistory(steps); len(history) != 0 {
			t.Errorf("input %q produced %+v, want no execution record", input, history)
		}
	}
}

// TestEndWithoutAStartIsIgnored pins the `continue` on an unmatched end. An end
// step whose start was never recorded describes a command this task cannot be
// shown to have launched; folding it in would credit an execution with no argv
// and no provenance.
func TestEndWithoutAStartIsIgnored(t *testing.T) {
	steps := []subagents.ToolCallStep{
		{ToolCallID: "orphan", Name: "run_command", Kind: "end", Output: "exit=0\n"},
		{ToolCallID: "c1", Name: "run_command", Kind: "start", Input: `{"argv":["make","verify"]}`},
		{ToolCallID: "c1", Name: "run_command", Kind: "end", Output: "exit=0\n"},
	}
	history := toolExecutionHistory(steps)
	if len(history) != 1 {
		t.Fatalf("history = %+v, want only the matched pair", history)
	}
	if history[0].CommandLine != "make verify" {
		t.Fatalf("history[0] = %+v, want the matched make verify record", history[0])
	}
}

// TestOneStartIsConsumedByOneEnd keeps a single start from being credited
// twice by a repeated end id.
func TestOneStartIsConsumedByOneEnd(t *testing.T) {
	steps := []subagents.ToolCallStep{
		{ToolCallID: "c1", Name: "run_command", Kind: "start", Input: `{"argv":["make","verify"]}`},
		{ToolCallID: "c1", Name: "run_command", Kind: "end", Output: "exit=0\n"},
		{ToolCallID: "c1", Name: "run_command", Kind: "end", Output: "exit=0\n"},
	}
	if history := toolExecutionHistory(steps); len(history) != 1 {
		t.Fatalf("history = %+v, want exactly one record for one start", history)
	}
}
