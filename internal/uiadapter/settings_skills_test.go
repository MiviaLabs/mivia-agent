package uiadapter

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

func setupSkillsTestEnv(t *testing.T) (string, *SettingsStore) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	userSkillsDir := workspace.UserSkillsDir()

	def := skills.Definition{
		Name:          "user-lint",
		Description:   "run user linter",
		UserInvocable: true,
		Tools:         []string{"run_command"},
		Instructions:  "# Lint\nRun linter.",
	}
	if err := skills.WriteSkillMarkdown(userSkillsDir, def); err != nil {
		t.Fatalf("WriteSkillMarkdown failed: %v", err)
	}

	store := NewSettingsStore(nil, nil, nil)
	return userSkillsDir, store
}

func TestSettingsSkills_ListAndToggle(t *testing.T) {
	_, store := setupSkillsTestEnv(t)

	// 1. Verify list contains the created skill
	list := store.Settings().Skills.Skills()
	var found *ports.SkillView
	for i := range list {
		if list[i].Name == "user-lint" {
			found = &list[i]
			break
		}
	}
	if found == nil || !found.UserInvocable {
		t.Fatalf("expected user-lint skill with UserInvocable=true in Skills()")
	}

	// 2. Toggle invocable
	h, err := store.Settings().Skills.Apply(context.Background(), ports.ScopeUser, ports.SetSkillUserInvocable{
		Name:   "user-lint",
		Origin: "user",
		On:     false,
	})
	if err != nil {
		t.Fatalf("Apply SetSkillUserInvocable failed: %v", err)
	}
	for range h.Events() {
	}

	listAfter := store.Settings().Skills.Skills()
	for i := range listAfter {
		if listAfter[i].Name == "user-lint" {
			if listAfter[i].UserInvocable {
				t.Error("expected UserInvocable = false after toggle")
			}
			break
		}
	}
}

func TestSettingsSkills_SaveAndRemove(t *testing.T) {
	userSkillsDir, store := setupSkillsTestEnv(t)

	// Save new skill
	h, err := store.Settings().Skills.Apply(context.Background(), ports.ScopeUser, ports.SaveSkill{
		Name:          "user-test",
		Origin:        "user",
		Description:   "run tests",
		UserInvocable: true,
		Tools:         []string{"run_command"},
		Instructions:  "# Test\nRun tests.",
	})
	if err != nil {
		t.Fatalf("Apply SaveSkill failed: %v", err)
	}
	for range h.Events() {
	}

	if _, err := os.Stat(filepath.Join(userSkillsDir, "user-test", "SKILL.md")); err != nil {
		t.Errorf("expected user-test SKILL.md to exist: %v", err)
	}

	// Remove skill
	h2, err := store.Settings().Skills.Apply(context.Background(), ports.ScopeUser, ports.RemoveSkill{
		Name:   "user-lint",
		Origin: "user",
	})
	if err != nil {
		t.Fatalf("Apply RemoveSkill failed: %v", err)
	}
	for range h2.Events() {
	}

	if _, err := os.Stat(filepath.Join(userSkillsDir, "user-lint")); !os.IsNotExist(err) {
		t.Errorf("expected user-lint directory to be deleted")
	}
}

func TestSettingsSkills_Errors(t *testing.T) {
	_, store := setupSkillsTestEnv(t)

	// Remove non-existent
	h1, err := store.Settings().Skills.Apply(context.Background(), ports.ScopeUser, ports.RemoveSkill{
		Name:   "non-existent",
		Origin: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	var last ports.SaveEvent
	for ev := range h1.Events() {
		last = ev
	}
	if last.State != ports.SaveFailed {
		t.Errorf("expected SaveFailed, got %v", last.State)
	}

	// Toggle non-existent
	h2, err := store.Settings().Skills.Apply(context.Background(), ports.ScopeUser, ports.SetSkillUserInvocable{
		Name:   "non-existent",
		Origin: "user",
		On:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for ev := range h2.Events() {
		last = ev
	}
	if last.State != ports.SaveFailed {
		t.Errorf("expected SaveFailed, got %v", last.State)
	}
}
