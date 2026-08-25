package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteSkillMarkdownAndLoad(t *testing.T) {
	tmp := t.TempDir()
	def := Definition{
		Name:             "test-custom",
		Description:      "custom skill description",
		ShortDescription: "short desc",
		ArgsHint:         "[target]",
		UserInvocable:    true,
		Triggers:         []string{"test", "run"},
		Tools:            []string{"read_file", "run_command"},
		Instructions:     "# Test Custom\nDo custom task.",
	}

	if err := WriteSkillMarkdown(tmp, def); err != nil {
		t.Fatalf("WriteSkillMarkdown failed: %v", err)
	}

	reg, err := loadMarkdown(tmp)
	if err != nil {
		t.Fatalf("loadMarkdown failed: %v", err)
	}
	loaded, ok := reg.Get("test-custom")
	if !ok {
		t.Fatal("expected test-custom skill in registry")
	}
	if loaded.Description != def.Description {
		t.Errorf("Description = %q, want %q", loaded.Description, def.Description)
	}
	if !loaded.UserInvocable {
		t.Errorf("UserInvocable = false, want true")
	}
	if len(loaded.Triggers) != 2 {
		t.Errorf("Triggers len = %d, want 2", len(loaded.Triggers))
	}

	// Update invocable
	if err := UpdateSkillUserInvocable(tmp, "test-custom", false); err != nil {
		t.Fatalf("UpdateSkillUserInvocable failed: %v", err)
	}
	reg2, err := loadMarkdown(tmp)
	if err != nil {
		t.Fatalf("loadMarkdown failed: %v", err)
	}
	loaded2, ok := reg2.Get("test-custom")
	if !ok || loaded2.UserInvocable {
		t.Errorf("expected UserInvocable to be false after update, got ok=%v invocable=%v", ok, loaded2.UserInvocable)
	}

	// Remove skill
	if err := RemoveSkillDirectory(tmp, "test-custom"); err != nil {
		t.Fatalf("RemoveSkillDirectory failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "test-custom")); !os.IsNotExist(err) {
		t.Errorf("expected skill directory to be deleted")
	}
}
