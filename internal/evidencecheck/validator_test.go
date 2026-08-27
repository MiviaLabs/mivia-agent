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

func TestBuildClaim_HeaderFiltering(t *testing.T) {
	if isHeader("ReportFormat") != true {
		t.Error("expected ReportFormat to be header")
	}
	if isHeader("make verify") != false {
		t.Error("expected make verify not to be header")
	}

	_, ok := buildClaim("ReportFormat: mivia-report/v1", "ReportFormat", "mivia-report/v1", "")
	if ok {
		t.Error("expected header not to be built as claim")
	}

	_, ok = buildClaim("   ", "", "PASS", "")
	if ok {
		t.Error("expected empty command not to be built as claim")
	}

	_, ok = buildClaim("make verify", "make verify", "", "")
	if ok {
		t.Error("expected empty verdict not to be built as claim")
	}
}

func TestNormalizeArgv(t *testing.T) {
	// Normalize empty
	if len(normalizeArgv(nil)) != 0 {
		t.Error("expected empty")
	}
	if len(normalizeArgv([]string{""})) != 0 {
		t.Error("expected empty for blanks")
	}

	// Bash wrapper
	bashWrapped := normalizeArgv([]string{"bash", "-c", "make verify"})
	if len(bashWrapped) != 2 || bashWrapped[0] != "make" || bashWrapped[1] != "verify" {
		t.Errorf("expected normalized bash wrapper to be [make verify], got %v", bashWrapped)
	}

	// Sh wrapper
	shWrapped := normalizeArgv([]string{"sh", "-c", "pytest tests/"})
	if len(shWrapped) != 2 || shWrapped[0] != "pytest" || shWrapped[1] != "tests/" {
		t.Errorf("expected normalized sh wrapper, got %v", shWrapped)
	}

	// /usr/bin/zsh wrapper
	zshWrapped := normalizeArgv([]string{"/usr/bin/zsh", "-c", "cargo test"})
	if len(zshWrapped) != 2 || zshWrapped[0] != "cargo" || zshWrapped[1] != "test" {
		t.Errorf("expected normalized zsh wrapper, got %v", zshWrapped)
	}
}

func TestMatchesArgv_Basic(t *testing.T) {
	// matchesArgv empty
	if matchesArgv(nil, []string{"make"}) != false {
		t.Error("expected false for empty claim")
	}
	if matchesArgv([]string{"make"}, nil) != false {
		t.Error("expected false for empty executed")
	}

	// program mismatch
	if matchesArgv([]string{"make"}, []string{"pytest"}) != false {
		t.Error("expected false for program mismatch")
	}
	if matchesArgv([]string{"./custom_python_tool", "run"}, []string{"./other_tool", "run"}) != false {
		t.Error("expected false for non-matching binary")
	}

	// python variations
	if matchesArgv([]string{"python", "test.py"}, []string{"python3", "test.py"}) != true {
		t.Error("expected python and python3 to match")
	}
	if matchesArgv([]string{"python3", "test.py"}, []string{"python", "test.py"}) != true {
		t.Error("expected python3 and python to match")
	}

	// Single token match
	if matchesArgv([]string{"make"}, []string{"make"}) != true {
		t.Error("expected single token match")
	}

	// Case sensitivity & path base
	if matchesArgv([]string{"./bin/make", "VERIFY"}, []string{"make", "verify"}) != true {
		t.Error("expected path base and case insensitive match")
	}

	// Substring / token match
	if matchesArgv([]string{"go", "test", "-race"}, []string{"go", "test", "-race", "./..."}) != true {
		t.Error("expected substring match")
	}
}

func TestMatchesArgv_Security(t *testing.T) {
	// Explicit dry-run match: when claim explicitly has dry-run flag
	if matchesArgv([]string{"make", "-n", "verify"}, []string{"make", "-n", "verify"}) != true {
		t.Error("expected explicit dry-run match")
	}

	// Leading flags in claim and executed
	if matchesArgv([]string{"go", "-v", "test"}, []string{"go", "-v", "test", "./..."}) != true {
		t.Error("expected leading flag match")
	}

	// Exact token vs partial prefix token: make verify must NOT match make verify-agent or make verify-fast
	if matchesArgv([]string{"make", "verify"}, []string{"make", "verify-agent"}) != false {
		t.Error("expected make verify NOT to match make verify-agent")
	}
	if matchesArgv([]string{"make", "verify"}, []string{"make", "verify-fast"}) != false {
		t.Error("expected make verify NOT to match make verify-fast")
	}

	// Dry run rejection: make -n verify must NOT satisfy make verify
	if matchesArgv([]string{"make", "verify"}, []string{"make", "-n", "verify"}) != false {
		t.Error("expected dry-run make -n verify NOT to satisfy make verify")
	}

	// Claim has more arguments than executed
	if matchesArgv([]string{"make", "verify", "extra", "flag"}, []string{"make", "verify"}) != false {
		t.Error("expected false when claim has more arguments than executed")
	}

	// Argument token mismatch inside loop
	if matchesArgv([]string{"make", "build"}, []string{"make", "clean"}) != false {
		t.Error("expected false when arguments do not match")
	}

	// Loop exhaustion with no match
	if matchesArgv([]string{"go", "test", "pkg1"}, []string{"go", "test", "pkg2", "pkg3"}) != false {
		t.Error("expected false when argument subsequence is not found")
	}

	// Subcommand mismatch: git commit -m "status" must NOT satisfy git status
	if matchesArgv([]string{"git", "status"}, []string{"git", "commit", "-m", "status"}) != false {
		t.Error("expected git commit NOT to satisfy git status")
	}
}

func TestSummaryNotice_And_Error(t *testing.T) {
	rep := ValidationReport{Valid: true}
	if rep.SummaryNotice() != "" {
		t.Errorf("expected empty notice on valid report, got %q", rep.SummaryNotice())
	}
	if err := rep.Error(); err != nil {
		t.Errorf("expected nil error on valid report, got %v", err)
	}

	invalidRep := ValidationReport{
		Valid: false,
		UnexecutedClaims: []Claim{
			{Command: "make verify", ClaimedVerdict: "PASS"},
		},
	}
	if err := invalidRep.Error(); err == nil {
		t.Error("expected non-nil error on invalid report")
	}

	// Fail closed if Valid is true but slices have entries
	inconsistentRep := ValidationReport{
		Valid: true,
		UnexecutedClaims: []Claim{
			{Command: "make verify", ClaimedVerdict: "PASS"},
		},
	}
	if err := inconsistentRep.Error(); err == nil {
		t.Error("expected non-nil error on inconsistent report")
	}
}
