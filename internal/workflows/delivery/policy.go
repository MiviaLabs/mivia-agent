package delivery

import (
	"fmt"
	"unicode"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/template"
)

// MaxTitleBytes bounds one rendered pull-request title.
const MaxTitleBytes = 4096

// MaxCommitBytes bounds one rendered commit message.
const MaxCommitBytes = 32768

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
func (p Policy) RenderTitle(inputs map[string]string) (string, error) {
	return renderTemplate(p.TitleTemplate, inputs, MaxTitleBytes, false)
}

// RenderCommitMessage renders commit_message_template against the admitted inputs.
func (p Policy) RenderCommitMessage(inputs map[string]string) (string, error) {
	return renderTemplate(p.CommitMessageTemplate, inputs, MaxCommitBytes, true)
}

// renderTemplate expands the template against the admitted inputs, bounds both
// a single binding and the full render, and rejects NUL/control characters in
// the result (newlines are allowed only in commit messages).
func renderTemplate(src string, inputs map[string]string, maxBytes int, allowNewline bool) (string, error) {
	anyInputs := make(map[string]any, len(inputs))
	for k, v := range inputs {
		anyInputs[k] = v
	}
	rendered, err := template.Render(src, anyInputs, nil, maxBytes, maxBytes)
	if err != nil {
		return "", err
	}
	for _, r := range rendered {
		if r == '\n' && allowNewline {
			continue
		}
		if unicode.IsControl(r) {
			return "", fmt.Errorf("rendered template contains control character %q", r)
		}
	}
	return rendered, nil
}
