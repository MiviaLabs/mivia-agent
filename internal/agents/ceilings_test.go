package agents

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// Phase 3 of the agent model routing plan: turn count and resource ceilings
// are independent. max_turns = 0 stays unlimited iterations, but an agent must
// still be bounded in wall-clock time and provider spend.

func TestUnlimitedTurnsStillCarryCeilings(t *testing.T) {
	unlimited := 0
	reg, _, err := ResolveAll([]ResolveInput{{
		Name: "worker", Source: config.AgentSourceUser,
		Path: "/home/u/.mivia/agents/worker.toml",
		Spec: config.AgentFileSpec{
			Name: strp("worker"), Description: strp("d"), Tools: slicep("read_file"),
			MaxTurns: &unlimited, TimeoutSeconds: intp(900), MaxTokens: intp(4096),
		},
	}}, baseOpts())
	if err != nil {
		t.Fatal(err)
	}
	got, _ := reg.Get("worker")
	if got.MaxTurns == nil || *got.MaxTurns != 0 {
		t.Fatalf("max_turns = %#v, want unlimited sentinel 0", got.MaxTurns)
	}
	if got.TimeoutSeconds == nil || *got.TimeoutSeconds != 900 {
		t.Fatalf("timeout_seconds = %#v", got.TimeoutSeconds)
	}
	if got.MaxTokens == nil || *got.MaxTokens != 4096 {
		t.Fatalf("max_tokens = %#v", got.MaxTokens)
	}
}

// Ceilings inherit and override individually - unlike the provider/model pair
// they are not a unit, because each bounds a different resource.
func TestCeilingsInheritIndividually(t *testing.T) {
	reg, _, err := ResolveAll([]ResolveInput{
		{
			Name: "parent", Source: config.AgentSourceUser,
			Path: "/home/u/.mivia/agents/parent.toml",
			Spec: config.AgentFileSpec{
				Name: strp("parent"), Description: strp("d"), Tools: slicep("read_file"),
				TimeoutSeconds: intp(600), MaxTokens: intp(2048),
			},
		},
		{
			Name: "child", Source: config.AgentSourceUser,
			Path: "/home/u/.mivia/agents/child.toml",
			Spec: config.AgentFileSpec{
				Name: strp("child"), Description: strp("d"), Inherits: strp("parent"),
				TimeoutSeconds: intp(120),
			},
		},
	}, baseOpts())
	if err != nil {
		t.Fatal(err)
	}
	child, _ := reg.Get("child")
	if child.TimeoutSeconds == nil || *child.TimeoutSeconds != 120 {
		t.Fatalf("child timeout = %#v", child.TimeoutSeconds)
	}
	if child.MaxTokens == nil || *child.MaxTokens != 2048 {
		t.Fatalf("child must inherit the parent token ceiling, got %#v", child.MaxTokens)
	}
}

// Ceilings are part of the definition identity: work recorded under an agent
// that could spend 4096 tokens must not silently resume under one that can
// spend ten times that.
func TestCeilingsChangeDigest(t *testing.T) {
	base := ResolvedAgent{Name: "a"}
	baseDigest, err := base.DefinitionDigest()
	if err != nil {
		t.Fatal(err)
	}
	for name, bound := range map[string]ResolvedAgent{
		"timeout": {Name: "a", TimeoutSeconds: intp(60)},
		"tokens":  {Name: "a", MaxTokens: intp(60)},
	} {
		digest, err := bound.DefinitionDigest()
		if err != nil {
			t.Fatal(err)
		}
		if digest == baseDigest {
			t.Fatalf("%s ceiling must be part of the definition identity", name)
		}
	}
}

func TestNonPositiveCeilingsRejected(t *testing.T) {
	for key, body := range map[string]string{
		"timeout_seconds": "name = \"a\"\ndescription = \"d\"\ntimeout_seconds = 0\n",
		"max_tokens":      "name = \"a\"\ndescription = \"d\"\nmax_tokens = -1\n",
	} {
		_, _, err := config.ParseAgentFileTOML([]byte(body), "a.toml")
		if err == nil || !strings.Contains(err.Error(), key) {
			t.Fatalf("%s must be rejected when not positive, got %v", key, err)
		}
	}
}
