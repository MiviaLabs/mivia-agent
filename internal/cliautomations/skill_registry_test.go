// skill_registry_test.go pins the headless CLI's skill-registry fallback:
// automation.Config.SkillRegistry must resolve the SAME skills the
// interactive session's binding freezes in, because composition-built
// headless sessions carry no registry of their own - without the
// fallback every StepSkill dispatch in `automations run`/`serve` failed
// with "no skill registry available".
package cliautomations

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSkillRegistrySourceResolvesProjectSkills seeds one project skill
// under .agents/skills and asserts the source resolves it with its body
// (not a frontmatter stub), so a StepSkill naming it renders real
// instructions.
func TestSkillRegistrySourceResolvesProjectSkills(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, ".agents", "skills", "echo-probe")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := "---\nname: echo-probe\ndescription: test probe skill\nuser-invocable: true\n---\n\nDo the echo probe now.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}

	reg := skillRegistrySource(root)()
	if reg == nil {
		t.Fatal("skillRegistrySource returned a nil registry; StepSkill dispatch would fail with no-skill-registry")
	}
	found := false
	for _, def := range reg.List() {
		if def.Name == "echo-probe" {
			found = true
			if def.Instructions == "" {
				t.Fatal("echo-probe resolved with empty Instructions; the step would send nothing")
			}
			if !def.UserInvocable {
				t.Fatal("echo-probe resolved as not user-invocable; an automation step naming it would be refused")
			}
		}
	}
	if !found {
		t.Fatal("echo-probe not in the registry; project skills must be allowed for automation steps")
	}
}

// TestSkillRegistrySourceEmptyOnBrokenRoot pins the failure shape: a
// root whose skills cannot load yields an empty registry (the
// executor's own "not recognized" refusal names the skill), never a
// panic.
func TestSkillRegistrySourceEmptyOnBrokenRoot(t *testing.T) {
	// A root that is a FILE makes the project skills directory
	// unopenable; the loader warns and returns what it has.
	rootFile := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(rootFile, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	reg := skillRegistrySource(rootFile)()
	if reg == nil {
		return // nil is equally safe: the executor's named refusal
	}
	if got := len(reg.List()); got != 0 {
		t.Fatalf("broken root returned %d skills, want 0", got)
	}
}
