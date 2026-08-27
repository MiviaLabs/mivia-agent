package cliworkflow

// skill_resource_helpers_test.go duplicates cli's skill resource fixtures
// (skill_messaging_injection_test.go): a "review" skill with one resource.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/skills"
)

func resourceSkillRegistry(t *testing.T) *skills.Registry {
	t.Helper()
	return resourceSkillRegistryAt(t, t.TempDir())
}

func resourceSkillRegistryAt(t *testing.T, root string) *skills.Registry {
	t.Helper()
	dir := filepath.Join(root, "review")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"SKILL.md":       "---\nname: review\n---\nReview the change.\n",
		"resources.toml": "format = 1\n\n[[resources]]\nid = \"template\"\npath = \"template.md\"\nsummary = \"Report template\"\n",
		"template.md":    "TEMPLATE",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	reg, _, err := skills.LoadMarkdownSources([]skills.Source{{Dir: root, Origin: skills.OriginProject}}, skills.LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return reg
}
