package delivery

import (
	"context"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/redact"
	ledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// validatePRMetadata resolves the agent-provided PR metadata (title and
// summary) from the run's change-summary output, or falls back to the legacy
// title_template render, then validates the final title against the OPTIONAL
// workspace PR-title policy. It returns the title and body the PR creation
// will use. A metadata defect is a PRMetadataError; a policy-file defect is a
// RefusalError. The stage runs BEFORE any commit or push, so a metadata
// defect writes no delivery record.
func validatePRMetadata(ctx context.Context, repo ledger.Repository, req Request) (title, body string, err error) {
	summary, err := ResolveLatestChangeSummary(ctx, repo, req.RunID)
	if err != nil {
		return "", "", err
	}
	agentTitle, _ := summary["pr_title"].(string)
	agentSummary, _ := summary["pr_summary"].(string)
	switch {
	case strings.TrimSpace(agentTitle) != "":
		title, err = sanitizeAgentTitle(agentTitle)
		if err != nil {
			return "", "", err
		}
	case req.Policy.TitleTemplate == "":
		// An empty template is legal and renders no title.
		title = ""
	default:
		title, err = req.Policy.RenderTitle(req.Inputs)
		if err != nil {
			return "", "", err
		}
	}
	// Sanitize the agent summary BEFORE policy validation and body assembly,
	// so the exact string that passed validation is the string that is
	// published. A control character is a PRMetadataError; the surviving
	// summary is redacted. An empty summary passes through unchanged.
	agentSummary, err = sanitizeAgentSummary(agentSummary)
	if err != nil {
		return "", "", err
	}
	pol, err := LoadPRTitlePolicy(req.GitCtx.Dir, req.Policy.PRTitlePolicyPath)
	if err != nil {
		return "", "", err
	}
	if pol != nil {
		if verr := pol.Validate(title, agentSummary); verr != nil {
			return "", "", verr
		}
	}
	if strings.TrimSpace(agentSummary) != "" {
		body = agentSummary + "\n\n---\nAutomated workflow delivery from Mivia.\nRun: " + req.RunID + "\nWorkflow digest: " + req.WorkflowDigest
	} else {
		body = "Automated workflow delivery from Mivia.\n\nRun: " + req.RunID + "\nWorkflow digest: " + req.WorkflowDigest
	}
	return title, body, nil
}

// sanitizeAgentSummary makes an agent-provided summary safe for the PR body.
// The PR body is a multi-line field (gh --body= takes one argv element), so a
// line break is a legitimate part of the summary: LF is kept and CRLF is
// normalized to LF. Every OTHER control character is a PRMetadataError: the
// agent must fix the summary, so it is never silently altered. The surviving
// summary is redacted, mirroring the renderTemplate pipeline order. Unlike
// the title, the summary is NOT folded to one line.
func sanitizeAgentSummary(summary string) (string, error) {
	normalized := strings.ReplaceAll(summary, "\r\n", "\n")
	for _, r := range normalized {
		if r == '\n' {
			continue
		}
		if unicode.IsControl(r) {
			return "", &PRMetadataError{Reason: fmt.Sprintf("delivery: pr_summary contains control character %q; fix the agent-provided summary", r)}
		}
	}
	return redact.Text(normalized), nil
}

// sanitizeAgentTitle makes an agent-provided title safe for the PR title
// field. A control character or a title over GitHub's 256-rune ceiling is a
// PRMetadataError: the agent must fix the title, so it is never truncated.
// The surviving title is folded to one line and redacted, mirroring the
// renderTemplate pipeline order.
func sanitizeAgentTitle(title string) (string, error) {
	for _, r := range title {
		if unicode.IsControl(r) {
			return "", &PRMetadataError{Reason: fmt.Sprintf("delivery: pr_title contains control character %q; fix the agent-provided title", r)}
		}
	}
	if n := utf8.RuneCountInString(title); n > MaxTitleRunes {
		return "", &PRMetadataError{Reason: fmt.Sprintf("delivery: pr_title has %d characters, exceeding GitHub's %d-character limit; fix the agent-provided title", n, MaxTitleRunes)}
	}
	return redact.Text(foldToSingleLine(title)), nil
}
