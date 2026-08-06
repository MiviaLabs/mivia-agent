package delivery

import (
	"fmt"
	"unicode"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/template"
)

// DefaultMaxTitleBytes is the default limit for rendered pull-request titles.
// Zero or negative values in the workflow TOML are replaced by this default.
const DefaultMaxTitleBytes = 65536

// DefaultMaxCommitMessageBytes is the default limit for rendered commit messages.
// Zero or negative values in the workflow TOML are replaced by this default.
const DefaultMaxCommitMessageBytes = 1048576

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
// If the rendered result exceeds MaxTitleBytes, it is truncated at a
// word boundary (or byte boundary if no space exists near the limit).
func (p Policy) RenderTitle(inputs map[string]string) (string, error) {
	max := clampMax(p.MaxTitleBytes, DefaultMaxTitleBytes)
	return renderTemplate(p.TitleTemplate, inputs, max, false)
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
		// unbounded output and truncate ourselves.
		rendered, err = template.Render(src, anyInputs, nil, unboundedBinding, 0)
		if err != nil {
			return "", err
		}
	}
	for _, r := range rendered {
		if r == '\n' && allowNewline {
			continue
		}
		if unicode.IsControl(r) {
			return "", fmt.Errorf("rendered template contains control character %q", r)
		}
	}
	if len(rendered) <= maxBytes {
		return rendered, nil
	}
	return truncateRendered(rendered, maxBytes, allowNewline)
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
		// Byte-level truncation for commit messages.
		truncated := rendered[:maxBytes]
		if len(truncated) > 3 {
			truncated = truncated[:len(truncated)-3] + "..."
		}
		return truncated, nil
	}
	// Word-boundary truncation for titles.
	cut := maxBytes
	if cut > len(rendered) {
		cut = len(rendered)
	}
	// Try to break at the last space within the limit.
	if lastSpace := findLastSpace(rendered, cut-1); lastSpace > 0 {
		cut = lastSpace
	}
	return rendered[:cut], nil
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
