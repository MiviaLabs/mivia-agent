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
// A workspace definition may not select a provider at all. Workspace agent
// files are untrusted, gated content (INV-AG-29), and a provider name is not
// provider-local the way a model name is: any checked-out repository could
// otherwise ship an agent that redirects the operator's prompts, tool results,
// and file contents to a different vendor's endpoint, authenticated with the
// operator's own credentials. Model alone stays permitted because it can only
// select within the provider the user already chose.
func checkResolvedBinding(in ResolveInput, fields inheritedFields) error {
	if fields.provider == "" {
		return nil
	}
	if in.Source != config.AgentSourceUser {
		return fmt.Errorf("agent %q: %s definition may not select provider %q (provider selection is reserved for user-trusted definitions in ~/.mivia/agents)",
			in.Name, in.Source, fields.provider)
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
