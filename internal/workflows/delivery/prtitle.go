// PRTitlePolicy loads and applies the OPTIONAL workspace PR-title policy
// (.mivia/policy/pr-title.toml) before a delivery PR is created. The loader
// mirrors the commit-message policy pattern in policy.go: an absent file
// validates nothing, and every config defect is a permanent RefusalError.
//
// Validation failures are a different class of error. A title or summary that
// violates the policy is REPAIRABLE: the agent can change the metadata and
// retry, so Validate returns PRMetadataError, never RefusalError. The caller
// routes a PRMetadataError back to the agent for a fix and routes a
// RefusalError to a settled delivery_failed.
package delivery

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/redact"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
	"github.com/pelletier/go-toml/v2"
)

// DefaultPRTitlePolicyPath is the OPTIONAL workspace policy file consulted
// before a delivery PR is created. It is only read when present: a workspace
// that configures nothing is unaffected.
const DefaultPRTitlePolicyPath = workspace.Namespace + "/policy/pr-title.toml"

// maxPRTitlePatternBytes is the ceiling for a compiled title pattern. A larger
// pattern is a config defect, not a metadata defect.
const maxPRTitlePatternBytes = 2048

// PRTitlePolicy is the parsed shape of the OPTIONAL workspace PR-title policy.
type PRTitlePolicy struct {
	Title   TitleRule   `toml:"title"`
	Summary SummaryRule `toml:"summary"`
}

// TitleRule holds the title-side rules of the PR-title policy. A zero or
// negative numeric bound is unset and means UNLIMITED. An empty Pattern and an
// empty Scopes list disable those rules.
type TitleRule struct {
	Pattern  string   `toml:"pattern"`
	MinChars int      `toml:"min_chars"`
	MaxChars int      `toml:"max_chars"`
	Scopes   []string `toml:"scopes"`
}

// SummaryRule holds the summary-side rules of the PR-title policy. A zero or
// negative numeric bound is unset and means UNLIMITED. Required defaults to
// false when absent.
type SummaryRule struct {
	Required     bool `toml:"required"`
	MinChars     int  `toml:"min_chars"`
	MaxChars     int  `toml:"max_chars"`
	MinSentences int  `toml:"min_sentences"`
	MaxSentences int  `toml:"max_sentences"`
}

// PRMetadataError marks a REPAIRABLE metadata defect in a PR title or summary.
// The agent can fix the metadata and retry, so it is never a RefusalError.
// A RefusalError is permanent; a PRMetadataError returns the run to the agent
// for a metadata fix.
type PRMetadataError struct{ Reason string }

// Error implements error.
func (e *PRMetadataError) Error() string { return e.Reason }

// IsPRMetadataError reports whether err is a PRMetadataError (possibly
// wrapped).
func IsPRMetadataError(err error) bool {
	var me *PRMetadataError
	return errors.As(err, &me)
}

// LoadPRTitlePolicy reads the OPTIONAL workspace PR-title policy, when
// present. policyPath is the workflow-declared pr_title_policy path relative
// to workspaceRoot; an empty policyPath selects the default
// DefaultPRTitlePolicyPath. The asymmetry is deliberate and must be kept: a
// missing DEFAULT policy returns (nil, nil) — an unconfigured workspace
// validates nothing (legacy behavior) — but a caller-declared EXPLICIT custom
// policy path that does not exist is a config error, not an absent policy, so
// it is a permanent RefusalError naming the declared file (declared config
// that is missing is a config error). A workflow that declares the DEFAULT
// path explicitly has opted in just the same: an explicit declaration, even
// when it equals the default, is a declared file. Any other read error, malformed TOML,
// or strict decode error is a permanent RefusalError too: each is a config
// condition the workspace must fix before delivery can ever pass.
func LoadPRTitlePolicy(workspaceRoot, policyPath string) (*PRTitlePolicy, error) {
	declared := policyPath != ""
	if policyPath == "" {
		policyPath = DefaultPRTitlePolicyPath
	}
	data, err := os.ReadFile(filepath.Join(workspaceRoot, policyPath))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) && !declared {
			return nil, nil
		}
		return nil, &RefusalError{Reason: fmt.Sprintf("delivery: cannot read %s: %v", policyPath, err)}
	}
	var pol PRTitlePolicy
	dec := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields()
	if err := dec.Decode(&pol); err != nil {
		return nil, &RefusalError{Reason: fmt.Sprintf("delivery: %s is not valid TOML: %v", policyPath, err)}
	}
	if err := pol.validateShape(policyPath); err != nil {
		return nil, err
	}
	return &pol, nil
}

// validateShape rejects malformed policy shapes. Every defect is permanent:
// the workspace must fix the policy file, so each is a RefusalError. path
// names the policy file the loader actually read (the default or the
// workflow-declared custom path). The pattern length check runs BEFORE
// compilation so an oversized pattern is reported as a length defect, not as
// a regexp defect.
func (p *PRTitlePolicy) validateShape(path string) error {
	if len(p.Title.Pattern) > maxPRTitlePatternBytes {
		return &RefusalError{Reason: fmt.Sprintf("delivery: %s: title.pattern is %d bytes, exceeding the maximum of %d", path, len(p.Title.Pattern), maxPRTitlePatternBytes)}
	}
	if p.Title.Pattern != "" {
		if _, err := regexp.Compile(p.Title.Pattern); err != nil {
			return &RefusalError{Reason: fmt.Sprintf("delivery: %s: title.pattern %q is not a valid regexp: %v", path, p.Title.Pattern, err)}
		}
	}
	for _, scope := range p.Title.Scopes {
		if strings.TrimSpace(scope) == "" {
			return &RefusalError{Reason: fmt.Sprintf("delivery: %s: title.scopes must contain only non-empty trimmed strings, got %q", path, scope)}
		}
	}
	if p.Title.MinChars > 0 && p.Title.MaxChars > 0 && p.Title.MinChars > p.Title.MaxChars {
		return &RefusalError{Reason: fmt.Sprintf("delivery: %s: title.min_chars %d exceeds title.max_chars %d", path, p.Title.MinChars, p.Title.MaxChars)}
	}
	if p.Summary.MinChars > 0 && p.Summary.MaxChars > 0 && p.Summary.MinChars > p.Summary.MaxChars {
		return &RefusalError{Reason: fmt.Sprintf("delivery: %s: summary.min_chars %d exceeds summary.max_chars %d", path, p.Summary.MinChars, p.Summary.MaxChars)}
	}
	if p.Summary.MinSentences > 0 && p.Summary.MaxSentences > 0 && p.Summary.MinSentences > p.Summary.MaxSentences {
		return &RefusalError{Reason: fmt.Sprintf("delivery: %s: summary.min_sentences %d exceeds summary.max_sentences %d", path, p.Summary.MinSentences, p.Summary.MaxSentences)}
	}
	return nil
}

// Validate checks title and summary against the policy rules in a fixed
// order: (a) a policy exists, so the title is non-empty; (b) the pattern
// matches; (c) the captured scope is in the scope list; (d) title rune
// bounds; (e) summary presence and rune bounds; (f) sentence bounds. It is
// deterministic and returns *PRMetadataError only, never RefusalError. Every
// hint names the violated rule, the rule value, the received value, and the
// field to fix (pr_title or pr_summary). Title and summary values are
// redacted before they are embedded in a hint.
func (p *PRTitlePolicy) Validate(title, summary string) error {
	if strings.TrimSpace(title) == "" {
		return &PRMetadataError{Reason: "delivery: pr_title is empty; the policy requires a non-empty title"}
	}
	if p.Title.Pattern != "" {
		re, err := regexp.Compile(p.Title.Pattern)
		if err != nil {
			return &PRMetadataError{Reason: fmt.Sprintf("delivery: pr_title pattern %q is not a valid regexp: %v", p.Title.Pattern, err)}
		}
		if !re.MatchString(title) {
			return &PRMetadataError{Reason: fmt.Sprintf("delivery: pr_title %q does not match pattern %q", redact.Text(title), p.Title.Pattern)}
		}
		if len(p.Title.Scopes) > 0 {
			if err := p.validateScope(re, title); err != nil {
				return err
			}
		}
	}
	if err := checkRuneBounds("pr_title", title, p.Title.MinChars, p.Title.MaxChars); err != nil {
		return err
	}
	if p.Summary.Required && strings.TrimSpace(summary) == "" {
		return &PRMetadataError{Reason: "delivery: pr_summary is empty; required is true"}
	}
	if err := checkRuneBounds("pr_summary", summary, p.Summary.MinChars, p.Summary.MaxChars); err != nil {
		return err
	}
	return checkSentenceBounds(summary, p.Summary.MinSentences, p.Summary.MaxSentences)
}

// validateScope enforces exact, case-sensitive membership of the pattern's
// named "scope" group in the configured scope list.
func (p *PRTitlePolicy) validateScope(re *regexp.Regexp, title string) error {
	idx := re.SubexpIndex("scope")
	if idx < 0 {
		return &PRMetadataError{Reason: fmt.Sprintf("delivery: pr_title pattern %q must define a named \"scope\" capture group when scopes are configured", p.Title.Pattern)}
	}
	m := re.FindStringSubmatch(title)
	scope := ""
	if m != nil && idx < len(m) {
		scope = m[idx]
	}
	for _, allowed := range p.Title.Scopes {
		if scope == allowed {
			return nil
		}
	}
	return &PRMetadataError{Reason: fmt.Sprintf("delivery: pr_title scope %q is not allowed; allowed scopes are %q", redact.Text(scope), p.Title.Scopes)}
}

// checkRuneBounds enforces the min/max rune-count bounds of a rule field. A
// zero or negative bound is unset and skips its check. field names the
// metadata field the hint tells the agent to fix.
func checkRuneBounds(field, value string, min, max int) error {
	n := utf8.RuneCountInString(value)
	if min > 0 && n < min {
		return &PRMetadataError{Reason: fmt.Sprintf("delivery: %s has %d characters; min_chars requires at least %d", field, n, min)}
	}
	if max > 0 && n > max {
		return &PRMetadataError{Reason: fmt.Sprintf("delivery: %s has %d characters; max_chars allows at most %d", field, n, max)}
	}
	return nil
}

// checkSentenceBounds enforces the min/max sentence-count bounds of the
// summary rule. A zero or negative bound is unset and skips its check.
func checkSentenceBounds(summary string, min, max int) error {
	if min <= 0 && max <= 0 {
		return nil
	}
	n := SentenceCount(summary)
	if min > 0 && n < min {
		return &PRMetadataError{Reason: fmt.Sprintf("delivery: pr_summary has %d sentences; min_sentences requires at least %d", n, min)}
	}
	if max > 0 && n > max {
		return &PRMetadataError{Reason: fmt.Sprintf("delivery: pr_summary has %d sentences; max_sentences allows at most %d", n, max)}
	}
	return nil
}

// SentenceCount counts the sentence boundaries in s. The rule is
// deterministic: a sentence boundary is a terminator ('.', '!', '?') followed
// by whitespace and an uppercase letter, or followed by end of text. The
// function counts boundaries on the trimmed text. Empty text has zero
// sentences. The rule handles abbreviations and version numbers correctly:
// 'e.g.', 'v1.2.3', and 'U.S.' do not split a sentence because their
// terminators are not followed by whitespace and an uppercase letter.
func SentenceCount(s string) int {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return 0
	}
	runes := []rune(trimmed)
	count := 0
	for i, r := range runes {
		if r != '.' && r != '!' && r != '?' {
			continue
		}
		if i == len(runes)-1 {
			count++
			continue
		}
		j := i + 1
		if j >= len(runes) || !unicode.IsSpace(runes[j]) {
			continue
		}
		for j < len(runes) && unicode.IsSpace(runes[j]) {
			j++
		}
		if j < len(runes) && unicode.IsUpper(runes[j]) {
			count++
		}
	}
	return count
}
