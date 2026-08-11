package config

// The configured model object. It is a CLOSED shape: every key is spelled out
// below and anything else is a hard error, so a typo cannot silently disable a
// setting the operator believes is applied.

import (
	"fmt"
	"strconv"

	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
	"github.com/pelletier/go-toml/v2/unstable"
)

// ModelSpec is one explicitly configured provider model and its physical
// context capacity. The name is provider-qualified by its containing group.
type ModelSpec struct {
	Name                string `toml:"name"`
	ContextWindowTokens int    `toml:"context_window_tokens"`
	MaxOutputTokens     int    `toml:"max_output_tokens,omitempty"`
	// ReasoningEfforts is the ordered set of reasoning levels this model
	// offers. Empty means the model has no reasoning surface. Order is
	// preserved because it is the order the /effort picker lists.
	ReasoningEfforts []reasoning.Level `toml:"reasoning_efforts,omitempty"`
	// Reasoning is this model's DEFAULT effort and must be a member of
	// ReasoningEfforts. Empty means the model ships with no reasoning field
	// sent, which a model that offers efforts may still choose - the user
	// opts in through /effort. Reasoning belongs to the model rather than to
	// [chat] because capabilities and value sets differ per model, so one
	// session-global value would be wrong for every model it did not match.
	Reasoning reasoning.Level `toml:"reasoning,omitempty"`
	// ReasoningDialect is this model's wire shape. Empty uses the provider's
	// vetted default where one exists; load refuses an active level with no
	// resolvable dialect rather than letting the key silently do nothing.
	ReasoningDialect reasoning.Dialect `toml:"reasoning_dialect,omitempty"`
}

// UnmarshalTOML enforces the narrow model object shape. A scalar model array
// is rejected instead of being silently treated as an empty catalog.
//
// It implements go-toml's unstable.Unmarshaler, which is unversioned by name
// and whose signature has already changed across releases (*unstable.Node in
// v2.2.3, []byte later). Dispatch is a runtime type assertion, so a dependency
// bump that changes it does not break the build: this method would simply stop
// being called, every check below would become dead code, and the closed model
// shape would silently reopen. Any go-toml upgrade must re-run the model shape
// tests, not just `go build`.
func (m *ModelSpec) UnmarshalTOML(value *unstable.Node) error {
	if value == nil || (value.Kind != unstable.InlineTable && value.Kind != unstable.Table) {
		return fmt.Errorf("model must be an object")
	}
	var spec ModelSpec
	for child := value.Child(); child != nil; child = child.Next() {
		keyName, err := modelKeyName(child)
		if err != nil {
			return err
		}
		if err := spec.decodeField(keyName, child.Value()); err != nil {
			return err
		}
	}
	*m = spec
	return nil
}

// modelKeyName returns the single key part naming a field of the closed model
// object. A dotted key such as `name.bogus` addresses a nested table this
// shape does not have; reading only its first part would set Name from a key
// the operator never wrote, which is worse than the typo it hides. A child
// with no key part cannot name a field either, so both are the same error.
func modelKeyName(child *unstable.Node) (string, error) {
	name := ""
	parts := 0
	for key := child.Key(); key.Next(); {
		parts++
		if node := key.Node(); node != nil {
			name = string(node.Data)
		}
	}
	if parts != 1 {
		return "", fmt.Errorf("invalid model object")
	}
	return name, nil
}

// modelFieldDecoders is the ONE declaration of the closed model object's key
// set. decodeField dispatches through it, and auditModelKeys diffs raw
// array-of-table entries against it, so the two TOML spellings of the same
// entry cannot disagree about which keys exist. A second hand-kept list of
// these names would drift the moment a field is added.
var modelFieldDecoders = map[string]func(*ModelSpec, *unstable.Node) error{
	"name":                  (*ModelSpec).setName,
	"context_window_tokens": (*ModelSpec).setContextWindowTokens,
	"max_output_tokens":     (*ModelSpec).setMaxOutputTokens,
	"reasoning":             (*ModelSpec).setReasoning,
	"reasoning_dialect":     (*ModelSpec).setReasoningDialect,
	"reasoning_efforts":     (*ModelSpec).setReasoningEfforts,
}

// knownModelKey reports whether a key names a field of the closed model
// object.
func knownModelKey(key string) bool {
	_, ok := modelFieldDecoders[key]
	return ok
}

// decodeField applies one key of the closed model object. Every unknown key is
// an error: a typo must not silently disable a setting the operator believes
// is applied.
func (m *ModelSpec) decodeField(key string, value *unstable.Node) error {
	decode, ok := modelFieldDecoders[key]
	if !ok {
		return unknownModelKeyError(key)
	}
	return decode(m, value)
}

func (m *ModelSpec) setName(value *unstable.Node) error {
	text, err := modelString(value)
	if err != nil {
		return err
	}
	m.Name = text
	return nil
}

func (m *ModelSpec) setContextWindowTokens(value *unstable.Node) error {
	parsed, err := modelInt(value)
	if err != nil {
		return err
	}
	m.ContextWindowTokens = parsed
	return nil
}

func (m *ModelSpec) setMaxOutputTokens(value *unstable.Node) error {
	parsed, err := modelInt(value)
	if err != nil {
		return err
	}
	m.MaxOutputTokens = parsed
	return nil
}

func (m *ModelSpec) setReasoning(value *unstable.Node) error {
	text, err := modelString(value)
	if err != nil {
		return err
	}
	level, err := reasoning.ParseLevel(text)
	if err != nil {
		return err
	}
	m.Reasoning = level
	return nil
}

func (m *ModelSpec) setReasoningDialect(value *unstable.Node) error {
	text, err := modelString(value)
	if err != nil {
		return err
	}
	dialect, err := reasoning.ParseDialect(text)
	if err != nil {
		return err
	}
	m.ReasoningDialect = dialect
	return nil
}

// setReasoningEfforts treats an explicitly empty array as the same statement as
// omitting the key, because decodeReasoningEfforts returns nil for it and
// "offers nothing" then has a single representation for every downstream check.
func (m *ModelSpec) setReasoningEfforts(value *unstable.Node) error {
	efforts, err := decodeReasoningEfforts(value)
	if err != nil {
		return err
	}
	m.ReasoningEfforts = efforts
	return nil
}

func modelString(value *unstable.Node) (string, error) {
	if value == nil || value.Kind != unstable.String {
		return "", fmt.Errorf("invalid model object")
	}
	return string(value.Data), nil
}

func modelInt(value *unstable.Node) (int, error) {
	if value == nil || value.Kind != unstable.Integer {
		return 0, fmt.Errorf("invalid model object")
	}
	parsed, err := strconv.Atoi(string(value.Data))
	if err != nil {
		return 0, fmt.Errorf("invalid model object")
	}
	return parsed, nil
}

// decodeReasoningEfforts reads the declared effort array. Only the TOML shape
// is enforced here; the level rules live in checkReasoningEfforts, which also
// runs for entries this decoder never sees.
func decodeReasoningEfforts(value *unstable.Node) ([]reasoning.Level, error) {
	if value == nil || value.Kind != unstable.Array {
		return nil, fmt.Errorf("reasoning_efforts must be an array of levels")
	}
	var efforts []reasoning.Level
	for item := value.Children(); item.Next(); {
		node := item.Node()
		if node == nil || node.Kind != unstable.String {
			return nil, fmt.Errorf("reasoning_efforts must be an array of levels")
		}
		efforts = append(efforts, reasoning.Level(node.Data))
	}
	if err := checkReasoningEfforts(efforts); err != nil {
		return nil, err
	}
	return efforts, nil
}

// activeModelProfile finds the configured model's own ModelSpec (context
// window, output cap, reasoning support), falling back to a bare profile
// with only the default context window when the active model was not
// declared under [providers.<name>].models - a provider-qualified name
// still resolves to a runtime, just without profile-specific limits.
func activeModelProfile(pc ProviderConfig, model string) ModelSpec {
	for _, profile := range pc.Models {
		if profile.Name == model {
			return profile
		}
	}
	return ModelSpec{Name: model, ContextWindowTokens: maxContextWindowTokens}
}
