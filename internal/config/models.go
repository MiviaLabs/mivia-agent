package config

// Model catalog normalization: the rules every configured model entry must
// satisfy before it can reach a provider client.

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
)

// NormalizeModelName canonicalizes a model identifier accepted from config,
// flags, slash commands, or persisted sessions. The error deliberately omits
// the supplied value because model identifiers reach terminal output.
func NormalizeModelName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("model name is empty")
	}
	if !utf8.ValidString(name) {
		return "", fmt.Errorf("model name is invalid")
	}
	if strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return "", fmt.Errorf("model name is invalid")
	}
	return name, nil
}

// checkReasoningIsDeliverable refuses a model whose reasoning level could
// never reach the wire. The dialect resolves request-first, then to the
// provider's vetted default; when neither exists the client fails closed and
// sends nothing, so accepting the key here would leave an operator with a
// setting that looks applied and does nothing. ModelSpec cannot see its
// provider during decode, which is why this runs where the groups are known.
//
// A dialect without a level is fine: it declares capability for a model that
// is currently dialled off and sends nothing on its own.
func checkReasoningIsDeliverable(provider string, model ModelSpec) error {
	if err := checkReasoningValues(model); err != nil {
		return err
	}
	if err := checkDefaultEffortIsOffered(model); err != nil {
		return err
	}
	// The gate is the CAPABILITY, not the default: /effort can activate any
	// declared level, so a model that offers efforts it could never deliver is
	// broken even when it ships with reasoning off.
	if !ModelOffersReasoning(model) {
		return nil
	}
	dialect := reasoning.Resolve(provider, ModelReasoning(model)).Dialect
	if dialect == "" {
		return fmt.Errorf(
			"model %q declares reasoning_efforts but provider %q has no default wire dialect; set reasoning_dialect on the model entry",
			model.Name, provider)
	}
	if dialect == reasoning.DialectNone {
		return fmt.Errorf(
			"model %q declares reasoning_efforts but its reasoning_dialect is %q, which sends nothing",
			model.Name, reasoning.DialectNone)
	}
	return checkDialectCanGradeTheSet(model, dialect)
}

// checkDialectCanGradeTheSet refuses a set the wire shape would flatten. A
// dialect that only switches thinking on or off encodes every graded level
// identically, so /effort would report a change no request ever carried - the
// same "looks applied, does nothing" failure the dialect check exists to stop.
// One graded level beside off is exactly what such a dialect expresses, so the
// gate is two or more distinct graded levels.
func checkDialectCanGradeTheSet(model ModelSpec, dialect reasoning.Dialect) error {
	if dialect.CanGrade() {
		return nil
	}
	graded := make(map[reasoning.Level]struct{}, len(model.ReasoningEfforts))
	for _, level := range model.ReasoningEfforts {
		if level != reasoning.Off {
			graded[level] = struct{}{}
		}
	}
	if len(graded) < 2 {
		return nil
	}
	return fmt.Errorf(
		"model %q declares graded reasoning_efforts (%s) but its reasoning_dialect %q only switches thinking on or off, so every level would send the same request; set reasoning_dialect = %q",
		model.Name, reasoning.FormatLevelsQuoted(model.ReasoningEfforts), dialect, reasoning.DialectThinkingEffort)
}

// checkReasoningValues re-validates the reasoning fields of a decoded model.
// ModelSpec.UnmarshalTOML is dispatched for inline tables only, so a model
// written as [[providers.x.models]] arrives carrying whatever strings the file
// held. Validation that fires for one TOML spelling is not validation, and
// this runs for both.
func checkReasoningValues(model ModelSpec) error {
	if _, err := reasoning.ParseLevel(string(model.Reasoning)); err != nil {
		return err
	}
	if _, err := reasoning.ParseDialect(string(model.ReasoningDialect)); err != nil {
		return err
	}
	return checkReasoningEfforts(model.ReasoningEfforts)
}

// checkReasoningEfforts holds the rules for the declared set: every entry is a
// known level, none is empty, and none repeats. A duplicate would render two
// identical rows in the /effort picker.
func checkReasoningEfforts(efforts []reasoning.Level) error {
	seen := make(map[reasoning.Level]struct{}, len(efforts))
	for _, level := range efforts {
		parsed, err := reasoning.ParseLevel(string(level))
		if err != nil {
			return err
		}
		if !parsed.Active() {
			return fmt.Errorf("reasoning_efforts must not contain an empty level")
		}
		if _, duplicate := seen[parsed]; duplicate {
			return fmt.Errorf("reasoning_efforts repeats %q", parsed)
		}
		seen[parsed] = struct{}{}
	}
	return nil
}

// checkDefaultEffortIsOffered keeps the declared set the single source of
// truth and the default a pointer into it. A default outside the set would be
// a value /effort could never return to, and a default with no set at all
// would be a second way to spell the same configuration.
func checkDefaultEffortIsOffered(model ModelSpec) error {
	if !model.Reasoning.Active() {
		return nil
	}
	if !ModelOffersReasoning(model) {
		return fmt.Errorf(
			"model %q sets reasoning = %q but declares no reasoning_efforts; list the levels it offers",
			model.Name, model.Reasoning)
	}
	if slices.Contains(model.ReasoningEfforts, model.Reasoning) {
		return nil
	}
	return fmt.Errorf(
		"model %q sets reasoning = %q which is not among its reasoning_efforts (%s)",
		model.Name, model.Reasoning, reasoning.FormatLevelsQuoted(model.ReasoningEfforts))
}

// ModelOffersReasoning reports whether this model declares any reasoning
// effort. It is the one predicate every surface asks - config validation, the
// /model annotation, and the /effort picker's empty state - so "offers
// nothing" cannot mean different things in different places.
func ModelOffersReasoning(spec ModelSpec) bool {
	return len(spec.ReasoningEfforts) > 0
}

func normalizeModels(in []ModelSpec, maxTokens int, provider string) ([]ModelSpec, error) {
	if len(in) == 0 {
		return nil, nil
	}
	if len(in) > maxProviderModels {
		return nil, fmt.Errorf("models has too many entries")
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]ModelSpec, 0, len(in))
	for i, model := range in {
		name, err := NormalizeModelName(model.Name)
		if err != nil {
			if strings.TrimSpace(model.Name) == "" {
				return nil, fmt.Errorf("models[%d] is empty", i)
			}
			return nil, fmt.Errorf("models[%d] is invalid", i)
		}
		if model.ContextWindowTokens < minContextWindowTokens || model.ContextWindowTokens > maxContextWindowTokens {
			return nil, fmt.Errorf("models[%d] has invalid context window", i)
		}
		if model.MaxOutputTokens < 0 || model.MaxOutputTokens >= model.ContextWindowTokens {
			return nil, fmt.Errorf("models[%d] has invalid max output tokens", i)
		}
		reserve := maxTokens
		if model.MaxOutputTokens > 0 && (reserve <= 0 || model.MaxOutputTokens < reserve) {
			reserve = model.MaxOutputTokens
		}
		if reserve > 0 && model.ContextWindowTokens <= reserve {
			return nil, fmt.Errorf("models[%d] context window is too small for max_tokens", i)
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, fmt.Errorf("models[%d] is a duplicate", i)
		}
		seen[name] = struct{}{}
		model.Name = name
		if err := checkReasoningIsDeliverable(provider, model); err != nil {
			return nil, err
		}
		out = append(out, model)
	}
	return out, nil
}

// ModelReasoning projects a model profile's reasoning pair as one value, so
// the request paths that carry it thread a single field instead of two that
// can drift apart.
func ModelReasoning(spec ModelSpec) reasoning.Setting {
	return reasoning.Setting{Level: spec.Reasoning, Dialect: spec.ReasoningDialect}
}
