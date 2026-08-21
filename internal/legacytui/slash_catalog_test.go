package legacytui

import (
	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/skills"
)

func TestSlashCatalogSeparatesSurfacesAndIncludesSearch(t *testing.T) {
	for _, name := range []string{"/help", "/clear", "/new", "/status", "/sessions", "/list", "/session", "/tools", "/plain", "/select", "/model", "/agent", "/budget", "/steps", "/save", "/load", "/delete", "/resume", "/search"} {
		if _, ok := cli.FindSlashCommand(name, cli.SlashSurfaceTUI, nil); !ok {
			t.Fatalf("TUI command %q missing from catalog", name)
		}
	}
	for _, name := range []string{"/exit", "/quit", "/q", "/provider", "/workspace"} {
		if _, ok := cli.FindSlashCommand(name, cli.SlashSurfacePlain, nil); !ok {
			t.Fatalf("plain command %q missing from catalog", name)
		}
		if _, ok := cli.FindSlashCommand(name, cli.SlashSurfaceTUI, nil); ok {
			t.Fatalf("plain-only command %q leaked into TUI catalog", name)
		}
	}
	if command, ok := cli.FindSlashCommand("/h", cli.SlashSurfaceTUI, nil); !ok || command.Name != "/help" || !command.AutoExecute {
		t.Fatalf("help alias = %#v ok=%v", command, ok)
	}
	if command, ok := cli.FindSlashCommand("/model", cli.SlashSurfaceTUI, nil); !ok || command.AutoExecute {
		t.Fatalf("model command = %#v ok=%v", command, ok)
	}
	if command, ok := cli.FindSlashCommand("/agent", cli.SlashSurfaceTUI, nil); !ok || command.AutoExecute {
		t.Fatalf("agent command = %#v ok=%v", command, ok)
	}
	if _, ok := cli.FindSlashCommand("/agent", cli.SlashSurfacePlain, nil); !ok {
		t.Fatal("plain catalog missing /agent")
	}
}

func TestSlashCatalogAddsInvocableSkillsAndRejectsBuiltinCollision(t *testing.T) {
	reg := skills.NewRegistry()
	for _, def := range []skills.Definition{
		{Name: "bug_audit", Origin: skills.OriginProject, Description: "Audit", UserInvocable: true, ArgsHint: "<path>"},
		{Name: "hidden", Origin: skills.OriginUser, Description: "Hidden", UserInvocable: false},
		{Name: "help", Origin: skills.OriginUser, Description: "Collision", UserInvocable: true},
	} {
		if err := reg.Register(def); err != nil {
			t.Fatal(err)
		}
	}
	command, ok := cli.FindSlashCommand("/bug-audit", cli.SlashSurfaceTUI, reg)
	if !ok || command.Kind != cli.SlashKindSkill || command.SkillName != "bug_audit" || command.ArgsHint != "<path>" {
		t.Fatalf("skill command = %#v ok=%v", command, ok)
	}
	if _, ok := cli.FindSlashCommand("/hidden", cli.SlashSurfaceTUI, reg); ok {
		t.Fatal("non-invocable skill reached slash catalog")
	}
	if command, ok := cli.FindSlashCommand("/help", cli.SlashSurfaceTUI, reg); !ok || command.Kind != cli.SlashKindBuiltin {
		t.Fatalf("builtin collision changed help command: %#v ok=%v", command, ok)
	}
}

func TestTUIHelpUsesSkillCatalog(t *testing.T) {
	reg := skills.NewRegistry()
	if err := reg.Register(skills.Definition{
		Name: "bug-audit", Description: "Audit defects", Origin: skills.OriginProject, UserInvocable: true,
	}); err != nil {
		t.Fatal(err)
	}
	lines := newHelpDialogFor(reg).lines
	joined := ""
	for _, line := range lines {
		joined += cli.StripANSI(line) + "\n"
	}
	if !strings.Contains(joined, "/bug-audit") || !strings.Contains(joined, "Audit defects") {
		t.Fatalf("catalog help missing skill:\n%s", joined)
	}
}

func TestSlashCatalogProjectWinsNormalizedSkillTokenCollision(t *testing.T) {
	reg := skills.NewRegistry()
	for _, definition := range []skills.Definition{
		{Name: "a-b", Origin: skills.OriginUser, UserInvocable: true},
		{Name: "a_b", Origin: skills.OriginProject, UserInvocable: true},
	} {
		if err := reg.Register(definition); err != nil {
			t.Fatal(err)
		}
	}
	var matches []cli.SlashCommand
	for _, command := range cli.SlashCommands(cli.SlashSurfaceTUI, reg) {
		if command.Name == "/a-b" {
			matches = append(matches, command)
		}
	}
	if len(matches) != 1 || matches[0].Origin != skills.OriginProject || matches[0].SkillName != "a_b" {
		t.Fatalf("normalized collision = %#v", matches)
	}
}

func TestLoadSessionSkillsMergesUserAndProjectScopes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := t.TempDir()
	write := func(base, name, description string) {
		t.Helper()
		dir := filepath.Join(base, ".mivia", "skills", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		body := "---\nname: " + name + "\ndescription: " + description + "\n---\nbody"
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(home, "review", "user")
	write(root, "review", "project")
	reg, warnings, err := cli.LoadSessionSkills(root, true)
	if err != nil {
		t.Fatal(err)
	}
	d, ok := reg.Get("review")
	if !ok || d.Origin != skills.OriginProject || d.Description != "project" {
		t.Fatalf("merged skill = %#v ok=%v", d, ok)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want one project shadow notice", warnings)
	}
}
