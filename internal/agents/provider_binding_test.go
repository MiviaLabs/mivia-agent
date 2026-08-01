package agents

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// Phase 1 of the agent model routing plan: an agent's execution target is one
// explicit (provider, model) binding, resolved and validated fail-closed.

// baselineDigestNoProvider is the digest an agent that declares no provider
// produced BEFORE Provider joined the definition. Routing snapshots persisted
// in the ledger carry these values, and resume re-validates them
// (internal/coordinator/recovery.go), so the payload must stay byte-identical
// for every definition that does not opt into a provider. Provider is
// therefore declared last in the digest struct and tagged omitempty.
const baselineDigestNoProvider = "sha256:0471f5fefd7be194a528995b7d1cca1d59fc3cd82a6473d8159fe6a95601ade2"

func TestDefinitionDigestUnchangedWithoutProvider(t *testing.T) {
	max := 0
	a := ResolvedAgent{
		Name: "researcher", Description: "d", Model: "glm-5.2",
		MaxTurns: &max, SystemPrompt: "p",
		EffectiveTools:  []string{"read_file", "grep"},
		DisallowedTools: []string{"run_command"},
	}
	got, err := a.DefinitionDigest()
	if err != nil {
		t.Fatal(err)
	}
	if got != baselineDigestNoProvider {
		t.Fatalf("digest for a provider-less agent changed:\n got %s\nwant %s\n"+
			"this invalidates every persisted routing snapshot; keep Provider last and omitempty", got, baselineDigestNoProvider)
	}
}

func TestDefinitionDigestChangesWithProvider(t *testing.T) {
	base := ResolvedAgent{Name: "a", Model: "m"}
	bound := ResolvedAgent{Name: "a", Provider: "zai", Model: "m"}
	baseDigest, err := base.DefinitionDigest()
	if err != nil {
		t.Fatal(err)
	}
	boundDigest, err := bound.DefinitionDigest()
	if err != nil {
		t.Fatal(err)
	}
	if baseDigest == boundDigest {
		t.Fatal("provider must be part of the definition identity")
	}
}

func TestResolveCarriesProvider(t *testing.T) {
	reg, _, err := ResolveAll([]ResolveInput{{
		Name:   "bound",
		Source: config.AgentSourceUser,
		Path:   "/home/u/.mivia/agents/bound.toml",
		Spec: config.AgentFileSpec{
			Name: strp("bound"), Description: strp("d"),
			Provider: strp("zai"), Model: strp("glm-5.2"),
			Tools: slicep("read_file"),
		},
	}}, baseOpts())
	if err != nil {
		t.Fatal(err)
	}
	got, ok := reg.Get("bound")
	if !ok {
		t.Fatal("agent not published")
	}
	if got.Provider != "zai" || got.Model != "glm-5.2" {
		t.Fatalf("binding = %q/%q", got.Provider, got.Model)
	}
}

// Provider selection is permitted from a workspace definition. This is an
// operator-accepted risk, recorded deliberately: a checked-out repository can
// route the operator's prompts and tool results to another vendor on the
// operator's credentials. The containment left is that the provider must
// already be configured with a credential in the operator's own config, which
// provider.NewForProvider enforces fail-closed.
func TestWorkspaceAgentMaySetProvider(t *testing.T) {
	reg, _, err := ResolveAll([]ResolveInput{{
		Name:   "workspace_bound",
		Source: config.AgentSourceWorkspace,
		Path:   "/repo/.mivia/agents/workspace_bound.toml",
		Spec: config.AgentFileSpec{
			Name: strp("workspace_bound"), Description: strp("d"),
			Provider: strp("deepseek"), Model: strp("deepseek-v4-pro"),
			Tools: slicep("read_file"),
		},
	}}, baseOpts())
	if err != nil {
		t.Fatalf("workspace agent may select a provider: %v", err)
	}
	got, _ := reg.Get("workspace_bound")
	if got.Provider != "deepseek" || got.Model != "deepseek-v4-pro" {
		t.Fatalf("binding = %q/%q", got.Provider, got.Model)
	}
}

// An unknown provider is still refused from any origin.
func TestWorkspaceAgentUnknownProviderStillRejected(t *testing.T) {
	_, _, err := ResolveAll([]ResolveInput{{
		Name:   "bogus",
		Source: config.AgentSourceWorkspace,
		Path:   "/repo/.mivia/agents/bogus.toml",
		Spec: config.AgentFileSpec{
			Name: strp("bogus"), Description: strp("d"),
			Provider: strp("not-a-provider"), Model: strp("m"),
			Tools: slicep("read_file"),
		},
	}}, baseOpts())
	if err == nil || !strings.Contains(err.Error(), "provider") {
		t.Fatalf("unknown provider must still be rejected, got %v", err)
	}
}

func TestWorkspaceAgentMaySetModel(t *testing.T) {
	_, _, err := ResolveAll([]ResolveInput{{
		Name:   "local",
		Source: config.AgentSourceWorkspace,
		Path:   "/repo/.mivia/agents/local.toml",
		Spec: config.AgentFileSpec{
			Name: strp("local"), Description: strp("d"),
			Model: strp("glm-5.2"), Tools: slicep("read_file"),
		},
	}}, baseOpts())
	if err != nil {
		t.Fatalf("workspace agent may still select a provider-local model: %v", err)
	}
}

// The binding is one unit. A child that restates only the model must not
// silently keep the parent's provider: that manufactures a (provider, model)
// pair no file ever authored, which is the ambiguity this phase removes.
func TestChildModelOverrideDropsInheritedProvider(t *testing.T) {
	reg, _, err := ResolveAll([]ResolveInput{
		{
			Name: "parent", Source: config.AgentSourceUser,
			Path: "/home/u/.mivia/agents/parent.toml",
			Spec: config.AgentFileSpec{
				Name: strp("parent"), Description: strp("d"),
				Provider: strp("zai"), Model: strp("glm-5.2"),
				Tools: slicep("read_file"),
			},
		},
		{
			Name: "child", Source: config.AgentSourceUser,
			Path: "/home/u/.mivia/agents/child.toml",
			Spec: config.AgentFileSpec{
				Name: strp("child"), Description: strp("d"),
				Inherits: strp("parent"), Model: strp("deepseek-v4-flash"),
			},
		},
	}, baseOpts())
	if err != nil {
		t.Fatal(err)
	}
	child, ok := reg.Get("child")
	if !ok {
		t.Fatal("child not published")
	}
	if child.Model != "deepseek-v4-flash" {
		t.Fatalf("child model = %q", child.Model)
	}
	if child.Provider != "" {
		t.Fatalf("child restated the model, so the parent provider must not carry over; got %q", child.Provider)
	}
}

// Restating neither key inherits the parent's binding whole.
func TestChildInheritsWholeBinding(t *testing.T) {
	reg, _, err := ResolveAll([]ResolveInput{
		{
			Name: "parent", Source: config.AgentSourceUser,
			Path: "/home/u/.mivia/agents/parent.toml",
			Spec: config.AgentFileSpec{
				Name: strp("parent"), Description: strp("d"),
				Provider: strp("zai"), Model: strp("glm-5.2"),
				Tools: slicep("read_file"),
			},
		},
		{
			Name: "child", Source: config.AgentSourceUser,
			Path: "/home/u/.mivia/agents/child.toml",
			Spec: config.AgentFileSpec{
				Name: strp("child"), Description: strp("d"),
				Inherits: strp("parent"),
			},
		},
	}, baseOpts())
	if err != nil {
		t.Fatal(err)
	}
	child, _ := reg.Get("child")
	if child.Provider != "zai" || child.Model != "glm-5.2" {
		t.Fatalf("inherited binding = %q/%q", child.Provider, child.Model)
	}
}

// Defense in depth: even if a spec reached the resolver without passing
// through ParseAgentFileTOML, a resolved provider with no model fails closed.
func TestResolvedProviderWithoutModelRejected(t *testing.T) {
	_, _, err := ResolveAll([]ResolveInput{{
		Name: "bad", Source: config.AgentSourceUser,
		Path: "/home/u/.mivia/agents/bad.toml",
		Spec: config.AgentFileSpec{
			Name: strp("bad"), Description: strp("d"),
			Provider: strp("zai"), Tools: slicep("read_file"),
		},
	}}, baseOpts())
	if err == nil || !strings.Contains(err.Error(), "model") {
		t.Fatalf("resolved provider without model must fail closed, got %v", err)
	}
}
