package evidencecheck

import (
	"regexp"
	"strings"
)

// Claim represents one extracted evidence assertion.
type Claim struct {
	RawText        string
	Command        string
	Argv           []string
	ClaimedVerdict string // "PASS", "FAIL", "NOT_RUN", "BLOCK"
	Note           string
}

var (
	evidenceLineRegex   = regexp.MustCompile(`(?i)^\s*[-*•]?\s*[` + "`" + `]?([^:` + "`" + `\n]+)[` + "`" + `]?\s*(?::|->)\s*(PASS|FAIL|NOT_RUN|BLOCK)\b(?:\s*[-–—]\s*(.*))?`)
	inlineEvidenceRegex = regexp.MustCompile(`(?i)\b(?:Evidence|Evidence:\s*)\s*[` + "`" + `]?([^:` + "`" + `\n]+)[` + "`" + `]?\s*(?::|->)\s*(PASS|FAIL|NOT_RUN|BLOCK)\b(?:\s*[-–—]\s*(.*))?`)
)

// ParseClaims extracts all evidence claims from markdown prose or mivia-report/v1 blocks.
func ParseClaims(text string) []Claim {
	var claims []Claim
	lines := strings.Split(text, "\n")
	inEvidenceSection := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		lower := strings.ToLower(trimmed)
		if strings.HasPrefix(lower, "evidence:") || strings.HasPrefix(lower, "## evidence") {
			inEvidenceSection = true
			if m := inlineEvidenceRegex.FindStringSubmatch(trimmed); len(m) > 2 {
				if c, ok := buildClaim(trimmed, m[1], m[2], getGroup(m, 3)); ok {
					claims = append(claims, c)
				}
			}
			continue
		}

		if inEvidenceSection && (strings.HasPrefix(lower, "#") || strings.HasPrefix(lower, "findings:") || strings.HasPrefix(lower, "residualrisk:") || strings.HasPrefix(lower, "nextaction:")) {
			inEvidenceSection = false
		}

		if inEvidenceSection {
			if m := evidenceLineRegex.FindStringSubmatch(trimmed); len(m) > 2 {
				if c, ok := buildClaim(trimmed, m[1], m[2], getGroup(m, 3)); ok {
					claims = append(claims, c)
				}
			}
		} else if strings.Contains(trimmed, "PASS") || strings.Contains(trimmed, "FAIL") {
			if m := inlineEvidenceRegex.FindStringSubmatch(trimmed); len(m) > 2 {
				if c, ok := buildClaim(trimmed, m[1], m[2], getGroup(m, 3)); ok {
					claims = append(claims, c)
				}
			}
		}
	}

	return claims
}

func getGroup(m []string, idx int) string {
	if idx < len(m) {
		return strings.TrimSpace(m[idx])
	}
	return ""
}

func buildClaim(raw, cmdStr, verdict, note string) (Claim, bool) {
	cmdStr = strings.Trim(strings.TrimSpace(cmdStr), "`'\"")
	verdict = strings.ToUpper(strings.TrimSpace(verdict))
	if cmdStr == "" || verdict == "" {
		return Claim{}, false
	}

	// Filter out non-command prose headers
	if isHeader(cmdStr) {
		return Claim{}, false
	}

	fields := strings.Fields(cmdStr)
	if len(fields) == 0 {
		return Claim{}, false
	}

	return Claim{
		RawText:        raw,
		Command:        cmdStr,
		Argv:           fields,
		ClaimedVerdict: verdict,
		Note:           note,
	}, true
}

func isHeader(s string) bool {
	return strings.EqualFold(s, "reportformat") ||
		strings.EqualFold(s, "skill") ||
		strings.EqualFold(s, "result") ||
		strings.EqualFold(s, "scope") ||
		strings.EqualFold(s, "summary")
}
