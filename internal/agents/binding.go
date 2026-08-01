package agents

import (
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/providerregistry"
)

// An agent's execution target is one explicit (provider, model) binding. A
// bare model name is ambiguous once more than one provider can expose the same
// name, so provider and model are resolved, inherited, and validated together.

// inheritBinding applies one authored spec over an inherited binding. Provider
// and model move as a unit: a child that restates either key replaces the pair
// wholesale, otherwise the parent's pair carries over intact. Inheriting the
// two fields separately would let a child's model land on a parent's provider,
// manufacturing a (provider, model) pair no file ever authored - exactly the
// ambiguity an explicit binding exists to remove.
func inheritBinding(spec config.AgentFileSpec, provider, model string) (string, string) {
	if spec.Provider == nil && spec.Model == nil {
		return provider, model
	}
	provider, model = "", ""
	if spec.Provider != nil {
		provider = strings.ToLower(strings.TrimSpace(*spec.Provider))
	}
	if spec.Model != nil {
		model = strings.TrimSpace(*spec.Model)
	}
	return provider, model
}

// checkResolvedBinding validates the (provider, model) pair AFTER inheritance.
// The parser sees one authored file and cannot see a pair assembled across an
// inheritance chain, so the invariant is enforced here; the parse-time rule
// stays as an early diagnostic.
//
// Provider selection is permitted from any trust origin, including workspace
// definitions. This is an accepted, operator-chosen risk: unlike a model name,
// a provider name is not session-local, so a checked-out repository can ship an
// agent that routes the operator's prompts, tool results, and file contents to
// a different vendor's endpoint, authenticated with the operator's own
// credentials. The remaining defences are that the provider must be configured
// in the operator's own config and must hold a credential there
// (provider.NewForProvider fails closed on both), so a workspace file can only
// select among endpoints the operator has already set up.
func checkResolvedBinding(in ResolveInput, fields inheritedFields) error {
	if fields.provider == "" {
		return nil
	}
	if strings.TrimSpace(fields.model) == "" {
		return fmt.Errorf("agent %q: provider %q requires a model (a provider with no model would pair a foreign endpoint with the session's model)",
			in.Name, fields.provider)
	}
	if _, known := providerregistry.Lookup(fields.provider); !known {
		return fmt.Errorf("agent %q: provider %q is not a known provider (available: %s)",
			in.Name, fields.provider, strings.Join(providerregistry.Names(), ", "))
	}
	return nil
}
