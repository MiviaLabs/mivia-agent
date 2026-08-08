package delivery

import (
	"encoding/json"
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
	"github.com/MiviaLabs/mivia-agent/internal/textutil"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/template"
)

// DefaultMaxTitleBytes is the default limit for rendered pull-request titles.
// Zero or negative values in the workflow TOML are replaced by this default.
const DefaultMaxTitleBytes = 65536

// DefaultMaxCommitMessageBytes is the default limit for rendered commit messages.
// Zero or negative values in the workflow TOML are replaced by this default.
const DefaultMaxCommitMessageBytes = 1048576

// MaxTitleRunes is GitHub's hard ceiling for pull-request titles, counted in
// characters (runes) rather than bytes. GitHub rejects titles longer than 256
// characters, so this is the effective ceiling for every rendered title even
// when MaxTitleBytes is configured higher: RenderTitle enforces BOTH limits
// (MaxTitleBytes as the byte cap, MaxTitleRunes as the character cap) and the
// stricter of the two wins.
const MaxTitleRunes = 256

// Policy is the snapshotted delivery policy of one workflow run. It is derived
// from the admitted compiled workflow (snapshot DefinitionTOML), never from a
// re-read of a changed file.
type Policy struct {
	Kind                  string
	Mode                  string
	Provider              string
	Base                  string
	TitleTemplate         string
	CommitMessageTemplate string
	MaxTitleBytes         int
	MaxCommitMessageBytes int
}

// clampMax returns v when positive, otherwise def.
func clampMax(v, def int) int {
	if v > 0 {
		return v
	}
	return def
}

// FromCompiled returns the delivery policy of a compiled workflow and whether
// publication is required (kind=pull_request and mode in draft|ready).
func FromCompiled(wf *compiler.CompiledWorkflow) (Policy, bool) {
	if wf == nil || !wf.DeliveryActive() {
		return Policy{}, false
	}
	d := wf.Delivery
	return Policy{
		Kind:                  d.Kind,
		Mode:                  d.Mode,
		Provider:              d.Provider,
		Base:                  d.Base,
		TitleTemplate:         d.TitleTemplate,
		CommitMessageTemplate: d.CommitMessageTemplate,
		MaxTitleBytes:         clampMax(d.MaxTitleBytes, DefaultMaxTitleBytes),
		MaxCommitMessageBytes: clampMax(d.MaxCommitMessageBytes, DefaultMaxCommitMessageBytes),
	}, true
}

// Validate rejects unsupported policy shapes. Returns an error suitable for a
// permanent delivery refusal.
func (p Policy) Validate() error {
	if p.Kind != "" && p.Kind != "pull_request" {
		return fmt.Errorf("delivery policy: kind %q is not supported (must be \"pull_request\" or empty)", p.Kind)
	}
	switch p.Mode {
	case "", "none", "draft", "ready":
	default:
		return fmt.Errorf("delivery policy: mode %q is not supported (must be one of: none, draft, ready)", p.Mode)
	}
	if p.Provider != "github" {
		return fmt.Errorf("delivery policy: provider %q is not supported (must be \"github\")", p.Provider)
	}
	if p.Kind == "pull_request" && p.Base == "" {
		return fmt.Errorf("delivery policy: base must be non-empty for pull_request delivery")
	}
	return nil
}

// RenderTitle renders title_template against the admitted inputs.
//
// Two ceilings apply and the stricter of the two wins:
//   - MaxTitleBytes (byte cap, default DefaultMaxTitleBytes): truncation at a
//     word boundary when a space exists, rune-safe otherwise.
//   - MaxTitleRunes (GitHub's hard 256-character limit, rune count): GitHub
//     rejects titles longer than 256 characters, so a rendered title is never
//     longer than MaxTitleRunes runes, regardless of MaxTitleBytes.
func (p Policy) RenderTitle(inputs map[string]string) (string, error) {
	max := clampMax(p.MaxTitleBytes, DefaultMaxTitleBytes)
	rendered, err := renderTemplate(p.TitleTemplate, inputs, max, false)
	if err != nil {
		return "", err
	}
	return truncateRunes(rendered, MaxTitleRunes), nil
}

// RenderCommitMessage renders commit_message_template against the admitted inputs.
// If the rendered result exceeds MaxCommitMessageBytes, it is truncated
// at a byte boundary with "..." appended.
func (p Policy) RenderCommitMessage(inputs map[string]string) (string, error) {
	max := clampMax(p.MaxCommitMessageBytes, DefaultMaxCommitMessageBytes)
	return renderTemplate(p.CommitMessageTemplate, inputs, max, true)
}

// renderTemplate expands the template against the admitted inputs, validates
// the result for NUL/control characters (newlines allowed only in commit
// messages), and truncates gracefully if the rendered output exceeds maxBytes.
func renderTemplate(src string, inputs map[string]string, maxBytes int, allowNewline bool) (string, error) {
	anyInputs := make(map[string]any, len(inputs))
	for k, v := range inputs {
		anyInputs[k] = v
	}
	// Use a high per-binding cap (higher than any sane rendered output) so
	// individual binding sizes never cause premature rejection. The overall
	// maxBytes is enforced after rendering via truncation.
	const unboundedBinding = 1 << 20 // 1 MiB per binding
	rendered, err := template.Render(src, anyInputs, nil, unboundedBinding, maxBytes)
	if err != nil {
		// The rendered-output cap inside template.Render is strict. Retry with
		// the high binding cap and truncate ourselves.
		rendered, err = template.Render(src, anyInputs, nil, unboundedBinding, unboundedBinding)
		if err != nil {
			return "", err
		}
	}
	if !allowNewline {
		rendered = foldToSingleLine(rendered)
	}
	for _, r := range rendered {
		if r == '\n' && allowNewline {
			continue
		}
		if unicode.IsControl(r) {
			return "", fmt.Errorf("rendered template contains control character %q", r)
		}
	}
	// Apply the workspace redaction policy (redact.Text is the identity when
	// no policy is installed) so a credential-shaped input never reaches
	// GitHub verbatim. Applied BEFORE truncation so truncating can never
	// re-expose a secret prefix.
	rendered = redact.Text(rendered)
	if len(rendered) <= maxBytes {
		return rendered, nil
	}
	return truncateRendered(rendered, maxBytes, allowNewline)
}

// foldToSingleLine makes rendered text safe for a single-line field.
//
// A pull-request title is one line. A template that interpolates a multi-line
// input therefore renders a value that no title field can hold. The previous
// behavior rejected that value and stopped delivery, which made a formatting
// detail of the INPUT block the whole run from publishing. A title that reads
// as one line is what the caller asked for, so this folds instead of refuses.
//
// The fold covers the whitespace control characters only: it turns each line
// break and tab into a space, then collapses each run of spaces into one. A
// control character that is not whitespace (NUL, an escape) still fails the
// check that follows, because such a character is a sign of corrupt or hostile
// input rather than of a multi-line message.
func foldToSingleLine(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	space := false
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' || r == ' ' {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteRune(' ')
		}
		space = false
		b.WriteRune(r)
	}
	return b.String()
}

// truncateRendered truncates rendered text to fit within maxBytes.
// For titles (single-line), it truncates at the nearest word boundary.
// For multi-line commit messages, it truncates at a byte boundary.
func truncateRendered(rendered string, maxBytes int, allowNewline bool) (string, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxTitleBytes
	}
	if len(rendered) <= maxBytes {
		return rendered, nil
	}
	if allowNewline {
		// Byte-level truncation for commit messages, rune-safe so a
		// multi-byte rune at the cut is never split.
		truncated := textutil.TruncateRuneSafe(rendered, maxBytes)
		if len(truncated) > 3 {
			truncated = truncated[:len(truncated)-3] + "..."
		}
		return truncated, nil
	}
	// Word-boundary truncation for titles. A space is one byte, so cutting at
	// a space is always a rune boundary.
	cut := maxBytes
	if cut > len(rendered) {
		cut = len(rendered)
	}
	if lastSpace := findLastSpace(rendered, cut-1); lastSpace > 0 {
		return rendered[:lastSpace], nil
	}
	// No space boundary (a long unbroken token or CJK text): truncate
	// rune-safely so the cut never splits a multi-byte rune, which would
	// otherwise publish invalid UTF-8 in the PR title.
	return textutil.TruncateRuneSafe(rendered, maxBytes), nil
}

// truncateRunes caps s to at most maxRunes runes, never splitting a rune. The
// cut is the byte length of the first maxRunes runes, applied through
// textutil.TruncateRuneSafe so the byte ceiling and the rune ceiling agree.
func truncateRunes(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	end := 0
	for i := 0; i < maxRunes; i++ {
		_, size := utf8.DecodeRuneInString(s[end:])
		end += size
	}
	return textutil.TruncateRuneSafe(s, end)
}

// findLastSpace returns the index of the last space character within the first
// limit bytes (inclusive), or 0 if none exists.
func findLastSpace(s string, limit int) int {
	end := limit + 1
	if end > len(s) {
		end = len(s)
	}
	for i := end - 1; i >= 0; i-- {
		if s[i] == ' ' {
			return i
		}
	}
	return 0
}

// commitMessagePolicyPath is the OPTIONAL workspace policy file consulted
// before a delivery commit, mirroring the repo's commit-msg hook. It is only
// read when present: a workspace that configures nothing is unaffected.
const commitMessagePolicyPath = ".mivia/policy/commit-message.json"

// commitMessagePolicy is the subset of the commit-message policy schema the
// delivery engine enforces generically. Repo-specific fields (types, scopes,
// scopeGuide, subjectRules) are intentionally not decoded: no workspace's
// scope or type list is ever compiled into this binary.
type commitMessagePolicy struct {
	RequireScope     bool `json:"requireScope"`
	MaxSubjectLength int  `json:"maxSubjectLength"`
}

// scopeSubjectRE matches the generic conventional-commit shape
// "type(scope): subject" (an optional "!" breaking-change marker is
// tolerated). Only the shape is checked; type and scope VALUES are the
// workspace's own commit-msg hook's business, never this binary's.
var scopeSubjectRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*\(([^()]+)\)(!)?: .+$`)

// ValidateCommitMessage validates the rendered commit message against the
// OPTIONAL workspace commit-message policy file
// (.mivia/policy/commit-message.json) under workspaceRoot, when present. An
// absent file validates nothing. A non-conforming subject, an unreadable
// policy file, or a malformed policy file is a permanent RefusalError: each is
// a condition the repo's commit-msg hook would reject forever, and refusing
// here (before any record write, commit, or push) turns the infinite
// delivery_pending loop into a settled delivery_failed with a clear reason.
func (p Policy) ValidateCommitMessage(workspaceRoot string, inputs map[string]string) error {
	data, err := os.ReadFile(filepath.Join(workspaceRoot, commitMessagePolicyPath))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return &RefusalError{Reason: fmt.Sprintf("delivery: cannot read %s: %v", commitMessagePolicyPath, err)}
	}
	var pol commitMessagePolicy
	if err := json.Unmarshal(data, &pol); err != nil {
		return &RefusalError{Reason: fmt.Sprintf("delivery: %s is not valid JSON: %v", commitMessagePolicyPath, err)}
	}
	msg, err := p.RenderCommitMessage(inputs)
	if err != nil {
		return err
	}
	return pol.validate(msg)
}

// validate checks the rendered commit message against the generic policy
// fields. The subject is the first non-empty, non-comment line, matching the
// commit-msg hook's subject_line semantics.
func (p commitMessagePolicy) validate(msg string) error {
	subject := commitSubject(msg)
	if p.MaxSubjectLength > 0 && utf8.RuneCountInString(subject) > p.MaxSubjectLength {
		return &RefusalError{Reason: fmt.Sprintf("delivery: commit message subject is %d characters, exceeding maxSubjectLength %d from %s", utf8.RuneCountInString(subject), p.MaxSubjectLength, commitMessagePolicyPath)}
	}
	if p.RequireScope && !scopeSubjectRE.MatchString(subject) {
		return &RefusalError{Reason: fmt.Sprintf("delivery: commit message subject %q does not satisfy requireScope from %s; expected type(scope): subject", subject, commitMessagePolicyPath)}
	}
	return nil
}

// commitSubject returns the first non-empty, non-comment line of the commit
// message, trimmed, mirroring the repo commit-msg hook's subject_line.
func commitSubject(msg string) string {
	for _, line := range strings.Split(msg, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		return line
	}
	return ""
}
