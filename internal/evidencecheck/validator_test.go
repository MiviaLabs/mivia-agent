package evidencecheck

import (
	"testing"
)

func TestParseClaims_MiviaReport(t *testing.T) {
	reportText := `
# Review
ReportFormat: mivia-report/v1
Skill: verify-change
Result: PASS
Scope: internal/evidencecheck
Summary: Verification checks passed
Evidence:
- make verify: PASS - offline verification gates passed
- go test -race ./...: PASS - race detector clean
- python3 scripts/validate_invariants.py: PASS
- manual check: NOT_RUN
Findings:
- none
ResidualRisk: none
NextAction: none
`
	claims := ParseClaims(reportText)
	if len(claims) != 4 {
		t.Fatalf("expected 4 claims, got %d", len(claims))
	}

	if claims[0].Command != "make verify" || claims[0].ClaimedVerdict != "PASS" {
		t.Errorf("claim 0 mismatch: %+v", claims[0])
	}
	if claims[1].Command != "go test -race ./..." || claims[1].ClaimedVerdict != "PASS" {
		t.Errorf("claim 1 mismatch: %+v", claims[1])
	}
	if claims[2].Command != "python3 scripts/validate_invariants.py" || claims[2].ClaimedVerdict != "PASS" {
		t.Errorf("claim 2 mismatch: %+v", claims[2])
	}
	if claims[3].Command != "manual check" || claims[3].ClaimedVerdict != "NOT_RUN" {
		t.Errorf("claim 3 mismatch: %+v", claims[3])
	}
}

func TestValidate_AllExecutedAndPassed(t *testing.T) {
	reportText := `
Evidence:
- make verify: PASS - all offline checks pass
- python3 scripts/test_validate_invariants.py: PASS
`
	history := []ToolExecutionRecord{
		{
			ToolName: "run_command",
			Argv:     []string{"make", "verify"},
			ExitCode: 0,
			Output:   "ok",
		},
		{
			ToolName: "run_command",
			Argv:     []string{"python3", "scripts/test_validate_invariants.py"},
			ExitCode: 0,
			Output:   "test_validate_invariants: ok",
		},
	}

	report := ValidateText(reportText, history)
	if !report.Valid {
		t.Fatalf("expected report to be valid, got invalid: %s", report.SummaryNotice())
	}
	if len(report.VerifiedClaims) != 2 {
		t.Errorf("expected 2 verified claims, got %d", len(report.VerifiedClaims))
	}
	if len(report.UnexecutedClaims) != 0 || len(report.FailedClaims) != 0 {
		t.Errorf("expected 0 unexecuted or failed claims, got %d unexec, %d failed", len(report.UnexecutedClaims), len(report.FailedClaims))
	}
}

func TestValidate_UnexecutedClaimFails(t *testing.T) {
	reportText := `
Evidence:
- make verify: PASS - fabricated claim
`
	history := []ToolExecutionRecord{
		{
			ToolName: "run_command",
			Argv:     []string{"go", "test", "./..."},
			ExitCode: 0,
			Output:   "PASS",
		},
	}

	report := ValidateText(reportText, history)
	if report.Valid {
		t.Fatal("expected report to be INVALID for unexecuted make verify claim")
	}
	if len(report.UnexecutedClaims) != 1 {
		t.Fatalf("expected 1 unexecuted claim, got %d", len(report.UnexecutedClaims))
	}
	if report.UnexecutedClaims[0].Command != "make verify" {
		t.Errorf("expected unexecuted command 'make verify', got %q", report.UnexecutedClaims[0].Command)
	}
	notice := report.SummaryNotice()
	if notice == "" {
		t.Error("expected non-empty summary notice")
	}
}

func TestValidate_FailedCommandClaimedAsPassFails(t *testing.T) {
	reportText := `
Evidence:
- make verify: PASS - claiming pass when it failed
`
	history := []ToolExecutionRecord{
		{
			ToolName: "run_command",
			Argv:     []string{"make", "verify"},
			ExitCode: 2,
			Output:   "FAIL: semgrep errors found",
		},
	}

	report := ValidateText(reportText, history)
	if report.Valid {
		t.Fatal("expected report to be INVALID for failed command claimed as PASS")
	}
	if len(report.FailedClaims) != 1 {
		t.Fatalf("expected 1 failed claim, got %d", len(report.FailedClaims))
	}
	if report.FailedClaims[0].ActualExit != 2 {
		t.Errorf("expected actual exit 2, got %d", report.FailedClaims[0].ActualExit)
	}
}

func TestValidate_RetryPassedUsesLatestExecution(t *testing.T) {
	reportText := `
Evidence:
- make verify: PASS
`
	// Command failed once, then was fixed and re-run successfully
	history := []ToolExecutionRecord{
		{
			ToolName: "run_command",
			Argv:     []string{"make", "verify"},
			ExitCode: 1,
			Output:   "initial failure",
		},
		{
			ToolName: "run_command",
			Argv:     []string{"make", "verify"},
			ExitCode: 0,
			Output:   "ok",
		},
	}

	report := ValidateText(reportText, history)
	if !report.Valid {
		t.Fatalf("expected report to be VALID after retry passed, got invalid: %s", report.SummaryNotice())
	}
	if len(report.VerifiedClaims) != 1 {
		t.Errorf("expected 1 verified claim, got %d", len(report.VerifiedClaims))
	}
}

func TestValidate_GenericEcosystems(t *testing.T) {
	testCases := []struct {
		name     string
		evidence string
		record   ToolExecutionRecord
		wantPass bool
	}{
		{
			name:     "Rust cargo test",
			evidence: "Evidence:\n- cargo test: PASS",
			record:   ToolExecutionRecord{ToolName: "run_command", Argv: []string{"cargo", "test", "--workspace"}, ExitCode: 0},
			wantPass: true,
		},
		{
			name:     "Python pytest",
			evidence: "Evidence:\n- pytest: PASS",
			record:   ToolExecutionRecord{ToolName: "run_command", Argv: []string{"pytest", "-v", "tests/"}, ExitCode: 0},
			wantPass: true,
		},
		{
			name:     "Node npm test",
			evidence: "Evidence:\n- npm test: PASS",
			record:   ToolExecutionRecord{ToolName: "run_command", Argv: []string{"npm", "test"}, ExitCode: 0},
			wantPass: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			report := ValidateText(tc.evidence, []ToolExecutionRecord{tc.record})
			if report.Valid != tc.wantPass {
				t.Errorf("got valid=%v, want=%v (notice: %s)", report.Valid, tc.wantPass, report.SummaryNotice())
			}
		})
	}
}
