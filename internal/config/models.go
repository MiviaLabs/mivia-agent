package config

// Model catalog normalization: the rules every configured model entry must
// satisfy before it can reach a provider client.

import (
	"fmt"
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
	if !model.Reasoning.Active() {
		return nil
	}
	dialect := model.ReasoningDialect
	if dialect == "" {
		var ok bool
		if dialect, ok = reasoning.DefaultDialect(provider); !ok {
			return fmt.Errorf(
				"model %q sets reasoning but provider %q has no default wire dialect; set reasoning_dialect on the model entry",
				model.Name, provider)
		}
	}
	if dialect == reasoning.DialectNone {
		return fmt.Errorf(
			"model %q sets reasoning but its reasoning_dialect is %q, which sends nothing",
			model.Name, reasoning.DialectNone)
	}
	return nil
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
