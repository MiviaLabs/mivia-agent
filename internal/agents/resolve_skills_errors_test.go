package agents

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// The refusal arms of a definition's skills allowlist. Each one stops a
// name that would otherwise reach a model-facing surface, so an arm that
// stopped refusing would widen what an agent can invoke rather than fail
// anything.

// TestAnEmptySkillsEntryIsRefused: a blank entry in the list is a typo or
// a stray comma, and accepting it would put an unnamed skill in the
// allowlist where later lookups resolve it to nothing.
func TestAnEmptySkillsEntryIsRefused(t *testing.T) {
	for _, raw := range []string{"", "   ", "\t"} {
		skills := []string{"real-skill", raw}
		_, _, err := resolveSkillsAllowlist("reviewer", &skills, ResolveOptions{})
		if err == nil {
			t.Fatalf("entry %q was accepted into the allowlist", raw)
		}
		if !strings.Contains(err.Error(), "must not be empty") {
			t.Errorf("error %q does not name the empty entry", err)
		}
		if !strings.Contains(err.Error(), "reviewer") {
			t.Errorf("error %q does not name the agent it came from", err)
		}
	}
}

// TestPickSkillOriginRefusesAnEntryThatIsNeitherUserNorProject pins the
// function's own floor. resolveSkillsAllowlist screens this shape out
// before calling, so the arm is reachable only directly - but it is the
// one that decides an origin, and an origin defaulting to "user" would
// let a workspace skill through the gate that exists to stop it.
func TestPickSkillOriginRefusesAnEntryThatIsNeitherUserNorProject(t *testing.T) {
	_, err := pickSkillOrigin("reviewer", "ghost", SkillCatalogueEntry{}, true)
	if err == nil {
		t.Fatal("an entry with no origin resolved to one")
	}
	if !strings.Contains(err.Error(), "unknown skill") || !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error %q does not name the unknown skill", err)
	}
}

// TestPickSkillOriginPrefersUserAndGatesProject is the positive contract
// the refusals guard: a user skill wins so a workspace one cannot shadow
// it, and a project-only skill needs the workspace gate open.
func TestPickSkillOriginPrefersUserAndGatesProject(t *testing.T) {
	both := SkillCatalogueEntry{User: true, Project: true}
	if got, err := pickSkillOrigin("a", "s", both, false); err != nil || got != string(config.AgentSourceUser) {
		t.Errorf("user+project resolved to (%q, %v), want the user origin", got, err)
	}

	projectOnly := SkillCatalogueEntry{Project: true}
	if _, err := pickSkillOrigin("a", "s", projectOnly, false); err == nil {
		t.Error("a project-only skill resolved with the workspace gate shut")
	} else if !strings.Contains(err.Error(), "workspace-only") {
		t.Errorf("error %q does not say why it was refused", err)
	}
	if got, err := pickSkillOrigin("a", "s", projectOnly, true); err != nil || got != string(config.AgentSourceWorkspace) {
		t.Errorf("project-only with the gate open resolved to (%q, %v), want the workspace origin", got, err)
	}
}
