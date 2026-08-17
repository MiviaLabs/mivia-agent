package delivery

import (
	"context"
	"fmt"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/redact"
	ledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// defaultMiviaBaseURL is the root of the Mivia web app, used to build links
// in published PR bodies. Override with the MIVIA_WEB_APP_BASE_URL env var.
const defaultMiviaBaseURL = "https://mivia.app"

// miviaBaseURL returns the configured Mivia web app root, falling back to
// defaultMiviaBaseURL when MIVIA_WEB_APP_BASE_URL is unset.
func miviaBaseURL() string {
	if v := os.Getenv("MIVIA_WEB_APP_BASE_URL"); v != "" {
		return v
	}
	return defaultMiviaBaseURL
}

// miviaAgentGitHubOwner and miviaAgentGitHubRepoURL identify the product on
// GitHub. github.com/<owner>.png serves the owner's avatar, so the attribution
// line can render the Mivia Agent avatar without a GitHub account for the
// commit email (GitHub markdown cannot resolve an avatar from an email).
const (
	miviaAgentGitHubOwner   = "MiviaLabs"
	miviaAgentGitHubRepoURL = "https://github.com/MiviaLabs/mivia-agent"
)

// attributionSummaryLabel is the <summary> label of the run-details block:
// the product avatar linked to the product repo, followed by a "Show
// details" affordance. Markdown does not render inside <summary>, so the
// link is an HTML anchor. <sub> keeps the line at footer visual weight.
func attributionSummaryLabel() string {
	avatar := "<a href=\"" + miviaAgentGitHubRepoURL + "\"><img src=\"https://github.com/" + miviaAgentGitHubOwner + ".png\" width=\"16\" height=\"16\" align=\"top\" alt=\"" + mviaCommitAuthorName + "\" /></a>"
	return "<sub>" + avatar + " Show details</sub>"
}

// shortDigest returns the display prefix of a workflow digest. The full
// 64-char hash wraps badly as link text; the link target keeps the full
// digest.
func shortDigest(digest string) string {
	if len(digest) > 12 {
		return digest[:12]
	}
	return digest
}

// runDetailsSection builds the collapsible "Mivia Agent run details" block:
// the run link, the workflow digest link, and (when the chunk carries one)
// its stack-part marker. The summary label renders in <sub> so the collapsed
// block stays at footer visual weight.
func runDetailsSection(base, runID, workflowDigest, stackPart string) string {
	runLink := "[" + runID + "](" + base + "/runs/" + runID + ")"
	digestLink := "[" + shortDigest(workflowDigest) + "](" + base + "/workflows/digest/" + workflowDigest + ")"
	body := "<details>\n<summary>" + attributionSummaryLabel() + "</summary>\n\n" +
		"- Run: " + runLink + "\n" +
		"- Workflow digest: " + digestLink + "\n"
	if strings.TrimSpace(stackPart) != "" {
		body += "- Stack part: " + stackPart + "\n"
	}
	return body + "\n</details>"
}

// deliveryFooter is the single collapsible attribution + run-details block
// appended to every PR body the delivery engine publishes. The summary line
// itself carries the authorship (avatar + linked name), so no separate
// attribution line or horizontal rule follows.
func deliveryFooter(runID, workflowDigest, stackPart string) string {
	return runDetailsSection(miviaBaseURL(), runID, workflowDigest, stackPart)
}

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
	// Append the host-owned "[stack k/N]" tag AFTER sanitization and policy
	// validation, mirroring how the body footer is appended after validation:
	// the agent-controlled title that passed validation stays intact and the
	// host adds the stack marker. An invalid stack_part or an over-limit
	// result is a repairable PRMetadataError.
	title, err = appendStackPartTitle(title, req.Inputs[InputStackPart])
	if err != nil {
		return "", "", err
	}
	footer := deliveryFooter(req.RunID, req.WorkflowDigest, req.Inputs[InputStackPart])
	if strings.TrimSpace(agentSummary) != "" {
		body = agentSummary + "\n\n" + footer
	} else {
		body = footer
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
