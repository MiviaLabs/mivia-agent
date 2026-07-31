package agents

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

func TestAgentSkillAllowlist_OmittedAllowsAll(t *testing.T) {
	opts := baseOpts()
	opts.SkillCatalogue = map[string]SkillCatalogueEntry{
		"bug-audit": {User: true},
		"review":    {User: true},
	}
	reg, _, err := ResolveAll([]ResolveInput{{
		Name: "root", Source: config.AgentSourceUser, Path: "root.toml",
		Spec: config.AgentFileSpec{
			Name: strp("root"), Description: strp("r"),
			Tools: slicep("read_file"),
			// Skills omitted
		},
	}}, opts)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := reg.Get("root")
	if a.Skills != nil {
		t.Fatalf("omitted skills must resolve to nil (all), got %#v", a.Skills)
	}
	if !SkillAllowed(&a, "bug-audit") || !SkillAllowed(&a, "anything") {
		t.Fatal("omitted skills must allow all")
	}
}

func TestAgentSkillAllowlist_EmptyAllowsNone(t *testing.T) {
	opts := baseOpts()
	opts.SkillCatalogue = map[string]SkillCatalogueEntry{"bug-audit": {User: true}}
	reg, _, err := ResolveAll([]ResolveInput{{
		Name: "locked", Source: config.AgentSourceUser, Path: "locked.toml",
		Spec: config.AgentFileSpec{
			Name: strp("locked"), Description: strp("l"),
			Tools:  slicep("read_file"),
			Skills: slicep(),
		},
	}}, opts)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := reg.Get("locked")
	if a.Skills == nil {
		t.Fatal("explicit [] must not become nil (all)")
	}
	if len(*a.Skills) != 0 {
		t.Fatalf("skills = %v", *a.Skills)
	}
	if SkillAllowed(&a, "bug-audit") {
		t.Fatal("empty skills must allow none")
	}
}

func TestAgentSkillsInherited(t *testing.T) {
	opts := baseOpts()
	opts.SkillCatalogue = map[string]SkillCatalogueEntry{
		"bug-audit": {User: true},
		"review":    {User: true},
	}
	reg, _, err := ResolveAll([]ResolveInput{
		{
			Name: "parent", Source: config.AgentSourceUser, Path: "parent.toml",
			Spec: config.AgentFileSpec{
				Name: strp("parent"), Description: strp("p"),
				Tools:  slicep("read_file"),
				Skills: slicep("bug-audit"),
			},
		},
		{
			Name: "child", Source: config.AgentSourceUser, Path: "child.toml",
			Spec: config.AgentFileSpec{
				Name: strp("child"), Description: strp("c"),
				Inherits: strp("parent"),
				// Skills omitted → inherit parent
			},
		},
	}, opts)
	if err != nil {
		t.Fatal(err)
	}
	child, _ := reg.Get("child")
	if child.Skills == nil || len(*child.Skills) != 1 || (*child.Skills)[0] != "bug-audit" {
		t.Fatalf("inherited skills = %#v", child.Skills)
	}
	if !SkillAllowed(&child, "bug-audit") || SkillAllowed(&child, "review") {
		t.Fatal("child must inherit parent allowlist only")
	}
}

func TestAgentSkillsInheritedOverride(t *testing.T) {
	opts := baseOpts()
	opts.SkillCatalogue = map[string]SkillCatalogueEntry{
		"bug-audit": {User: true},
		"review":    {User: true},
	}
	reg, _, err := ResolveAll([]ResolveInput{
		{
			Name: "parent", Source: config.AgentSourceUser, Path: "parent.toml",
			Spec: config.AgentFileSpec{
				Name: strp("parent"), Description: strp("p"),
				Tools:  slicep("read_file"),
				Skills: slicep("bug-audit"),
			},
		},
		{
			Name: "child", Source: config.AgentSourceUser, Path: "child.toml",
			Spec: config.AgentFileSpec{
				Name: strp("child"), Description: strp("c"),
				Inherits: strp("parent"),
				Skills:   slicep("review"),
			},
		},
	}, opts)
	if err != nil {
		t.Fatal(err)
	}
	child, _ := reg.Get("child")
	if !SkillAllowed(&child, "review") || SkillAllowed(&child, "bug-audit") {
		t.Fatalf("child override skills = %#v", child.Skills)
	}
}

func TestUnknownSkillRejected(t *testing.T) {
	opts := baseOpts()
	opts.SkillCatalogue = map[string]SkillCatalogueEntry{"known": {User: true}}
	_, _, err := ResolveAll([]ResolveInput{{
		Name: "a", Source: config.AgentSourceUser, Path: "a.toml",
		Spec: config.AgentFileSpec{
			Name: strp("a"), Description: strp("a"),
			Tools:  slicep("read_file"),
			Skills: slicep("missing-skill"),
		},
	}}, opts)
	if err == nil || !strings.Contains(err.Error(), "unknown skill") {
		t.Fatalf("expected unknown skill error, got %v", err)
	}
}

func TestWorkspaceSkillCannotShadowUserBinding(t *testing.T) {
	opts := baseOpts()
	opts.AllowProjectSkills = true
	opts.SkillCatalogue = map[string]SkillCatalogueEntry{
		"shared": {User: true, Project: true},
	}
	reg, _, err := ResolveAll([]ResolveInput{{
		Name: "a", Source: config.AgentSourceUser, Path: "a.toml",
		Spec: config.AgentFileSpec{
			Name: strp("a"), Description: strp("a"),
			Tools:  slicep("read_file"),
			Skills: slicep("shared"),
		},
	}}, opts)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := reg.Get("a")
	if a.SkillOrigins["shared"] != string(config.AgentSourceUser) {
		t.Fatalf("user skill must win over workspace shadow, origins=%v", a.SkillOrigins)
	}
}

func TestWorkspaceGateRequired(t *testing.T) {
	opts := baseOpts()
	opts.AllowProjectSkills = false
	opts.SkillCatalogue = map[string]SkillCatalogueEntry{
		"proj-only": {Project: true},
	}
	_, _, err := ResolveAll([]ResolveInput{{
		Name: "a", Source: config.AgentSourceUser, Path: "a.toml",
		Spec: config.AgentFileSpec{
			Name: strp("a"), Description: strp("a"),
			Tools:  slicep("read_file"),
			Skills: slicep("proj-only"),
		},
	}}, opts)
	if err == nil || !strings.Contains(err.Error(), "workspace-only") {
		t.Fatalf("expected workspace gate error, got %v", err)
	}
}

func TestAgentSkillAllowlist_SnapshotImmutable(t *testing.T) {
	opts := baseOpts()
	opts.SkillCatalogue = map[string]SkillCatalogueEntry{"bug-audit": {User: true}}
	reg, _, err := ResolveAll([]ResolveInput{{
		Name: "a", Source: config.AgentSourceUser, Path: "a.toml",
		Spec: config.AgentFileSpec{
			Name: strp("a"), Description: strp("a"),
			Tools:  slicep("read_file"),
			Skills: slicep("bug-audit"),
		},
	}}, opts)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := reg.Get("a")
	(*a.Skills)[0] = "mutated"
	a.SkillOrigins["bug-audit"] = "mutated"
	b, _ := reg.Get("a")
	if (*b.Skills)[0] == "mutated" {
		t.Fatal("registry skill allowlist mutated via returned clone")
	}
	if b.SkillOrigins["bug-audit"] == "mutated" {
		t.Fatal("registry skill origins mutated via returned clone")
	}
}

func TestCheckSkillInvocation_ToolsSubset(t *testing.T) {
	agent := &ResolvedAgent{
		Name:           "narrow",
		EffectiveTools: []string{"read_file"},
		Skills:         slicep("audit"),
	}
	if err := CheckSkillInvocation(agent, "audit", []string{"read_file"}); err != nil {
		t.Fatal(err)
	}
	err := CheckSkillInvocation(agent, "audit", []string{"read_file", "run_command"})
	if err == nil || !strings.Contains(err.Error(), "run_command") {
		t.Fatalf("expected tools subset failure, got %v", err)
	}
	err = CheckSkillInvocation(agent, "other", []string{"read_file"})
	if err == nil || !strings.Contains(err.Error(), "may not invoke") {
		t.Fatalf("expected allowlist failure, got %v", err)
	}
}
