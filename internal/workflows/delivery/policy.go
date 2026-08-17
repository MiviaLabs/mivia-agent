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
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/template"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
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

// DefaultMaxDeliveryRepairs is the default budget for the delivery -> repair
// -> success -> delivery cycle when the workflow does not configure
// delivery.max_repairs. It bounds how many times a delivery rejection may
// route back into the workflow's repair step before the run settles terminal
// (delivery_failed). The ceiling is deliberately higher than the original
// hard-coded 3: a gate that needs a couple of repair iterations (for example
// a config/code drift like a dialect the base does not yet implement) is
// common, while the run's duration cap still bounds the total spend.
const DefaultMaxDeliveryRepairs = 5

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
	// OnFailure names the step the run returns to when delivery fails for a
	// reason an agent can repair. Empty means the run holds for a person.
	OnFailure string
	// PRTitlePolicyPath is the workflow-relative path of the project PR-title
	// policy. Empty selects the default .mivia/policy/pr-title.toml.
	PRTitlePolicyPath string
	// OnPRMetadataFailure names the step that repairs PR-metadata failures.
	// Empty defaults to OnFailure.
	OnPRMetadataFailure string
	// OnDiffSizeFailure names the step that repairs an over-limit delivered
	// diff (a DiffSizeError). Empty defaults to OnFailure, which keeps
	// pre-existing stacking workflows on their declared generic repair step.
	OnDiffSizeFailure string
	// MaxRepairs bounds the delivery repair cycle for this run. Zero or a
	// negative value selects DefaultMaxDeliveryRepairs; the workflow TOML's
	// delivery.max_repairs sets it per workflow.
	MaxRepairs int
	// StackingHardLines is the resolved hard per-chunk diff-size limit
	// (added+deleted lines) when the workflow has a resolved stacking
	// configuration (CompiledWorkflow.Stacking). Zero means no stacking
	// config: delivery runs single-PR behavior with no size gate.
	StackingHardLines int
	// SplitDeferred mirrors StackingConfig.SplitDeferred (§5.2-5.3): when
	// true and a chunk's delivered diff exceeds StackingHardLines,
	// checkChunkDiffSize computes a host-side deterministic split instead of
	// returning a DiffSizeError. Opt-in (default false).
	SplitDeferred bool
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
	onPRMetadataFailure := d.OnPRMetadataFailure
	if onPRMetadataFailure == "" {
		onPRMetadataFailure = d.OnFailure
	}
	onDiffSizeFailure := d.OnDiffSizeFailure
	if onDiffSizeFailure == "" {
		onDiffSizeFailure = d.OnFailure
	}
	hardLines := 0
	splitDeferred := false
	if wf.Stacking != nil {
		hardLines = wf.Stacking.HardLines
		splitDeferred = wf.Stacking.SplitDeferred
	}
	return Policy{
		Kind:                  d.Kind,
		Mode:                  d.Mode,
		Provider:              d.Provider,
		Base:                  d.Base,
		TitleTemplate:         d.TitleTemplate,
		CommitMessageTemplate: d.CommitMessageTemplate,
		MaxTitleBytes:         clampMax(d.MaxTitleBytes, DefaultMaxTitleBytes),
		MaxCommitMessageBytes: clampMax(d.MaxCommitMessageBytes, DefaultMaxCommitMessageBytes),
		OnFailure:             d.OnFailure,
		PRTitlePolicyPath:     d.PRTitlePolicy,
		OnPRMetadataFailure:   onPRMetadataFailure,
		OnDiffSizeFailure:     onDiffSizeFailure,
		MaxRepairs:            clampMax(d.MaxRepairs, DefaultMaxDeliveryRepairs),
		StackingHardLines:     hardLines,
		SplitDeferred:         splitDeferred,
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
	if p.Provider != definition.ProviderGitHub {
		return fmt.Errorf("delivery policy: provider %q is not supported (must be %q)", p.Provider, definition.ProviderGitHub)
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
	// A template that HAS content but renders to whitespace folds to nothing.
	// Publishing a blank title is worse than saying so: the pull request would
	// carry no subject and the failure would surface as an opaque gh error.
	//
	// An empty template is a different case. It means the policy declares no
	// title, which is legal, so it keeps returning an empty string.
	if strings.TrimSpace(p.TitleTemplate) != "" && strings.TrimSpace(rendered) == "" {
		return "", fmt.Errorf("delivery policy: title_template rendered an empty title")
	}
	return truncateRunes(rendered, MaxTitleRunes), nil
}

// RenderCommitMessage renders commit_message_template against the admitted
// inputs. This is BODY content only (trailers, "Delivers: ..." context) -
// the commit SUBJECT line is always the agent's own pr_title (see
// buildCommitMessage in deliver_stage.go), never this template, so the
// workspace commit-message policy's subject rules are enforced against
// something the agent can actually edit. If the rendered result exceeds
// MaxCommitMessageBytes, it is truncated at a byte boundary with "..."
// appended.
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
		// Commit-message truncation, rune- and grapheme-safe so a multi-byte
		// rune, or a base character plus its combining mark, at the cut is
		// never split. The "..." marker bytes are reserved BEFORE the cut:
		// carving them out of the END of the prefix with a raw slice could
		// strip 3 bytes off a 4-byte rune that ended exactly at maxBytes,
		// leaving a dangling lead byte in the value that reaches
		// `git commit -m` (E1, DC-6).
		if maxBytes > 3 {
			return truncateBytesGraphemeSafe(rendered, maxBytes-3) + "...", nil
		}
		// The marker cannot fit; truncate at the raw cap. The caller still
		// learns truncation happened because the input exceeded maxBytes.
		return truncateBytesGraphemeSafe(rendered, maxBytes), nil
	}
	// Word-boundary truncation for titles. A space is one byte, so cutting at
	// a space is always a rune (and grapheme) boundary.
	cut := maxBytes
	if cut > len(rendered) {
		cut = len(rendered)
	}
	if lastSpace := findLastSpace(rendered, cut-1); lastSpace > 0 {
		return rendered[:lastSpace], nil
	}
	// No space boundary (a long unbroken token, CJK text, or decomposed
	// combining-mark text): truncate rune- and grapheme-safely so the cut
	// never splits a multi-byte rune (publishing invalid UTF-8) or strands a
	// base character without its combining mark (publishing valid but
	// visibly wrong text).
	return truncateBytesGraphemeSafe(rendered, maxBytes), nil
}

// truncateBytesGraphemeSafe truncates s to at most maxBytes bytes, first
// snapping the cut to a rune boundary (textutil.TruncateRuneSafe's existing
// guarantee), then backing it up further via backUntilGraphemeBoundary -
// the SAME grapheme-boundary logic truncateRunes uses (both operate on byte
// offsets into s), so a byte-capped truncation gets the identical guarantee
// a rune-capped one does: the result never strands a base character without
// its combining mark.
func truncateBytesGraphemeSafe(s string, maxBytes int) string {
	cut := len(textutil.TruncateRuneSafe(s, maxBytes))
	return s[:backUntilGraphemeBoundary(s, cut)]
}

// truncateRunes caps s to at most maxRunes runes, never splitting a rune, and
// never splitting a base character from a COMBINING MARK that renders as
// part of it (Unicode category Mn/Mc/Me, e.g. 'e' (U+0065) + COMBINING ACUTE
// ACCENT (U+0301), together "é"). Scoped deliberately to that one grapheme
// mechanism, not full Unicode grapheme-cluster segmentation (UAX #29): a
// ZWJ-joined emoji sequence or a regional-indicator flag pair is also a
// multi-rune grapheme but is NOT formed via a combining mark, so a cut can
// still split one of those. A rune boundary is not a combining-mark
// boundary: the initial cut is the byte length of the first maxRunes runes
// (agreeing with textutil.TruncateRuneSafe's rune-safety), but if the very
// next rune just past that cut is a combining mark, the cut split a
// character from its diacritic - keeping only the bare base character and
// silently dropping the mark(s), which is valid UTF-8 but visibly wrong text
// (worse for scripts where combining marks are semantically essential, not
// merely decorative). backUntilGraphemeBoundary backs the cut up past that
// base character (and any it stacks with) until the excluded remainder no
// longer starts with a mark, so the kept prefix always ends on a complete
// base+mark(s) grapheme, one character shorter rather than one mangled.
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
	return textutil.TruncateRuneSafe(s, backUntilGraphemeBoundary(s, end))
}

// backUntilGraphemeBoundary backs a rune-safe cut point up, one rune at a
// time, while the rune immediately past it is a combining mark (Unicode
// category Mn/Mc/Me) - i.e. while cutting there would strand that mark's
// base character without it. A character can carry more than one combining
// mark (stacked diacritics), so this backs up however many base characters
// that requires, not just one.
//
// If the entire prefix up to end is combining marks with no base character
// at all (malformed/unusual input: a leading run of marks), the naive
// backup would reach 0 and collapse a non-empty request into an EMPTY
// result - a behavior change from "truncate to at most N runes" to
// "silently return nothing" that the caller's own maxRunes>0 guard does not
// anticipate. There is no base character to back up TO in that case, so
// this returns the ORIGINAL end instead: a grapheme-unsafe cut is still
// strictly better than truncating a non-empty request down to nothing.
func backUntilGraphemeBoundary(s string, end int) int {
	original := end
	for end > 0 {
		next, nsize := utf8.DecodeRuneInString(s[end:])
		if nsize == 0 || !unicode.In(next, unicode.Mn, unicode.Mc, unicode.Me) {
			return end
		}
		_, lsize := utf8.DecodeLastRuneInString(s[:end])
		if lsize == 0 {
			return end
		}
		end -= lsize
	}
	return original
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
const commitMessagePolicyPath = workspace.Namespace + "/policy/commit-message.json"

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

// ValidateCommitSubject validates ONE commit subject line (the agent's own
// pr_title, already resolved and PR-title-policy-validated by
// validatePRMetadata - see deliver.go) against the OPTIONAL workspace
// commit-message policy file (.mivia/policy/commit-message.json) under
// workspaceRoot, when present. An absent file validates nothing. A
// non-conforming subject is a repairable PRMetadataError: the agent
// controls pr_title, so the SAME repair hint that already tells it to fix
// pr_title for the PR-title policy also fixes this - there is exactly one
// subject line and exactly one field the agent edits to change it. An
// unreadable or malformed policy file is a permanent RefusalError: that is a
// workspace configuration defect, not something any agent edit can fix.
func (p Policy) ValidateCommitSubject(workspaceRoot, subject string) error {
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
	return pol.validate(subject)
}

// validate checks a commit subject line against the generic policy fields.
func (p commitMessagePolicy) validate(subject string) error {
	subject = commitSubject(subject)
	if p.MaxSubjectLength > 0 && utf8.RuneCountInString(subject) > p.MaxSubjectLength {
		return &PRMetadataError{Reason: fmt.Sprintf("delivery: pr_title (used as the commit subject) is %d characters, exceeding maxSubjectLength %d from %s; shorten pr_title", utf8.RuneCountInString(subject), p.MaxSubjectLength, commitMessagePolicyPath)}
	}
	if p.RequireScope && !scopeSubjectRE.MatchString(subject) {
		return &PRMetadataError{Reason: fmt.Sprintf("delivery: pr_title (used as the commit subject) %q does not satisfy requireScope from %s; expected type(scope): subject - fix pr_title", subject, commitMessagePolicyPath)}
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
