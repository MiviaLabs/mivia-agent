package evidencecheck

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ToolExecutionRecord records one executed tool invocation in a turn or session.
type ToolExecutionRecord struct {
	ToolName    string   `json:"tool_name"`
	Argv        []string `json:"argv"`
	CommandLine string   `json:"command_line,omitempty"`
	ExitCode    int      `json:"exit_code"`
	Output      string   `json:"output,omitempty"`
}

// FailedClaim pairs a claimed PASS with the actual execution failure.
type FailedClaim struct {
	Claim       Claim
	ActualExit  int
	ErrorOutput string
}

// ValidationReport contains the outcome of cross-checking claims against tool execution records.
type ValidationReport struct {
	Valid            bool
	TotalClaims      int
	VerifiedClaims   []Claim
	UnexecutedClaims []Claim
	FailedClaims     []FailedClaim
	Notices          []string
}

// SummaryNotice returns a concise, human-readable summary of validation discrepancies.
func (r *ValidationReport) SummaryNotice() string {
	if r.Valid || (len(r.UnexecutedClaims) == 0 && len(r.FailedClaims) == 0) {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("[evidence_verification_warning]\n")
	for _, unexec := range r.UnexecutedClaims {
		sb.WriteString(fmt.Sprintf("  - Unexecuted Claim: %q claimed as %s but was never run in this session\n", unexec.Command, unexec.ClaimedVerdict))
	}
	for _, failed := range r.FailedClaims {
		sb.WriteString(fmt.Sprintf("  - Failed Claim: %q claimed as PASS but actually exited with code %d\n", failed.Claim.Command, failed.ActualExit))
	}
	return strings.TrimSpace(sb.String())
}

// Validate cross-checks extracted claims against recorded tool executions.
func Validate(claims []Claim, history []ToolExecutionRecord) ValidationReport {
	report := ValidationReport{
		Valid:       true,
		TotalClaims: len(claims),
	}

	for _, claim := range claims {
		// Only check PASS claims (NOT_RUN or FAIL claims are honest declarations of non-execution)
		if claim.ClaimedVerdict != "PASS" {
			report.VerifiedClaims = append(report.VerifiedClaims, claim)
			continue
		}

		matchedRecord, found := findMatchingExecution(claim, history)
		if !found {
			report.Valid = false
			report.UnexecutedClaims = append(report.UnexecutedClaims, claim)
			report.Notices = append(report.Notices, fmt.Sprintf("Command %q was claimed as PASS but was never executed.", claim.Command))
			continue
		}

		if matchedRecord.ExitCode != 0 {
			report.Valid = false
			report.FailedClaims = append(report.FailedClaims, FailedClaim{
				Claim:       claim,
				ActualExit:  matchedRecord.ExitCode,
				ErrorOutput: matchedRecord.Output,
			})
			report.Notices = append(report.Notices, fmt.Sprintf("Command %q was claimed as PASS but failed with exit code %d.", claim.Command, matchedRecord.ExitCode))
			continue
		}

		report.VerifiedClaims = append(report.VerifiedClaims, claim)
	}

	return report
}

// ValidateText parses and validates claims directly from response text.
func ValidateText(text string, history []ToolExecutionRecord) ValidationReport {
	claims := ParseClaims(text)
	return Validate(claims, history)
}

func findMatchingExecution(claim Claim, history []ToolExecutionRecord) (ToolExecutionRecord, bool) {
	claimArgv := normalizeArgv(claim.Argv)
	if len(claimArgv) == 0 {
		return ToolExecutionRecord{}, false
	}

	// Try exact match, then sub-command match from latest to earliest execution
	for i := len(history) - 1; i >= 0; i-- {
		rec := history[i]
		if rec.ToolName != "run_command" && rec.ToolName != "" {
			continue
		}

		recArgv := normalizeArgv(rec.Argv)
		if len(recArgv) == 0 && rec.CommandLine != "" {
			recArgv = normalizeArgv(strings.Fields(rec.CommandLine))
		}

		if matchesArgv(claimArgv, recArgv) {
			return rec, true
		}
	}

	return ToolExecutionRecord{}, false
}

func normalizeArgv(argv []string) []string {
	var out []string
	for _, arg := range argv {
		cleaned := strings.Trim(arg, "`'\" \t")
		if cleaned == "" {
			continue
		}
		// If argument contains sub-invocations like "bash -c 'make verify'"
		if (cleaned == "bash" || cleaned == "sh") && len(argv) >= 3 && argv[1] == "-c" {
			subArgs := strings.Fields(strings.Trim(strings.Join(argv[2:], " "), "`'\""))
			return normalizeArgv(subArgs)
		}
		out = append(out, cleaned)
	}
	return out
}

func matchesArgv(claim, executed []string) bool {
	if len(claim) == 0 || len(executed) == 0 {
		return false
	}

	// Exact binary/script match
	claimProg := filepath.Base(claim[0])
	execProg := filepath.Base(executed[0])

	if claimProg != execProg && !strings.EqualFold(claimProg, execProg) {
		// Handle python/python3 interchangeability
		if strings.HasPrefix(claimProg, "python") && strings.HasPrefix(execProg, "python") {
			// matches
		} else {
			return false
		}
	}

	if len(claim) == 1 && len(executed) == 1 {
		return true
	}

	// Check if claim arguments are a prefix or subset of executed arguments
	claimArgs := claim[1:]
	execArgs := executed[1:]

	// If claim is "make verify" and executed is "make verify", exact match
	if len(claimArgs) <= len(execArgs) {
		matchedAll := true
		for i, ca := range claimArgs {
			if !strings.EqualFold(ca, execArgs[i]) && filepath.Base(ca) != filepath.Base(execArgs[i]) {
				matchedAll = false
				break
			}
		}
		if matchedAll {
			return true
		}
	}

	// Substring / token matching for multi-word invocations
	claimStr := strings.Join(claim, " ")
	execStr := strings.Join(executed, " ")
	return strings.Contains(execStr, claimStr) || strings.Contains(claimStr, execStr)
}
