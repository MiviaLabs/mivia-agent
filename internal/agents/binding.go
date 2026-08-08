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
// The check runs on the authored pair regardless of trust origin, so an
// unknown provider or a provider without a model still fails closed from a
// workspace definition even though that pair would be stripped afterwards.
// User definitions always honor their (provider, model) selection. A
// workspace definition's selection is honored only when the operator opted in
// via AllowWorkspaceAgentProviders (credential-routing protection); otherwise
// resolve.go's materialize strips the pair and the agent inherits the session
// provider. Even under opt-in, the provider must be configured in the
// operator's own config and must hold a credential there
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

// stripWorkspaceBinding applies credential-routing protection: a workspace
// definition must not select a (provider, model) pair unless the operator
// opted in through the user-only [agents] gate. It reports the warning to
// print and whether the pair was stripped.
//
// The warning names the model as well as the provider. Stripping the pair
// while reporting only the provider let an operator believe a per-agent model
// was still in force: an agent file that pins one model for a review lens ran
// the session model instead, silently collapsing a deliberately decorrelated
// gate onto the model that produced the work. It also names the opt-in,
// because a warning an operator cannot act on is noise on every command.
