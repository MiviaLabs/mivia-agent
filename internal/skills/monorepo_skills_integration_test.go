package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMiviaMonorepoSkills(t *testing.T) {
	root := "../../../mivia-monorepo/.agents/skills"
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Skipf("monorepo skills not available: %v", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, e.Name(), "SKILL.md"))
		if err != nil {
			t.Errorf("%s: %v", e.Name(), err)
			continue
		}
		parsed, err := parseSkillMarkdown(data)
		if err != nil {
			t.Errorf("%s: parse failed: %v", e.Name(), err)
			continue
		}
		if parsed.name == "" {
			t.Errorf("%s: empty name", e.Name())
		}
	}
}
