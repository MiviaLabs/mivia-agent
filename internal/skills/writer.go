package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WriteSkillMarkdown serializes a skill definition with YAML frontmatter to <dir>/<name>/SKILL.md.
func WriteSkillMarkdown(dir string, def Definition) error {
	if def.Name == "" {
		return fmt.Errorf("skill name is required")
	}
	skillDir := filepath.Join(dir, def.Name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		return fmt.Errorf("create skill directory: %w", err)
	}

	var fm strings.Builder
	fm.WriteString("---\n")
	fm.WriteString(fmt.Sprintf("name: %s\n", def.Name))
	if def.Description != "" {
		fm.WriteString(fmt.Sprintf("description: %q\n", def.Description))
	}
	if def.ShortDescription != "" {
		fm.WriteString(fmt.Sprintf("short-description: %q\n", def.ShortDescription))
	}
	if def.ArgsHint != "" {
		fm.WriteString(fmt.Sprintf("argument-hint: %q\n", def.ArgsHint))
	}
	if !def.UserInvocable {
		fm.WriteString("user-invocable: false\n")
	} else {
		fm.WriteString("user-invocable: true\n")
	}
	if len(def.Triggers) > 0 {
		fm.WriteString("triggers:\n")
		for _, tr := range def.Triggers {
			fm.WriteString(fmt.Sprintf("  - %s\n", tr))
		}
	}
	if len(def.Tools) > 0 {
		fm.WriteString("tools:\n")
		for _, tool := range def.Tools {
			fm.WriteString(fmt.Sprintf("  - %s\n", tool))
		}
	}
	fm.WriteString("---\n")

	body := strings.TrimSpace(def.Instructions)
	if body == "" {
		body = "# " + def.Name + "\n"
	}
	content := fm.String() + "\n" + body + "\n"

	skillFile := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(skillFile, []byte(content), 0o600); err != nil {
		return fmt.Errorf("write SKILL.md: %w", err)
	}
	return nil
}

// RemoveSkillDirectory removes <dir>/<name> from disk.
func RemoveSkillDirectory(dir, name string) error {
	if name == "" {
		return fmt.Errorf("skill name is required")
	}
	skillDir := filepath.Join(dir, name)
	if err := os.RemoveAll(skillDir); err != nil {
		return fmt.Errorf("remove skill directory: %w", err)
	}
	return nil
}

// UpdateSkillUserInvocable toggles the user-invocable frontmatter flag in <dir>/<name>/SKILL.md.
func UpdateSkillUserInvocable(dir, name string, userInvocable bool) error {
	skillFile := filepath.Join(dir, name, "SKILL.md")
	data, err := os.ReadFile(skillFile)
	if err != nil {
		return fmt.Errorf("read SKILL.md: %w", err)
	}
	parsed, err := parseSkillMarkdown(data)
	if err != nil {
		return fmt.Errorf("parse SKILL.md: %w", err)
	}
	def := Definition{
		Name:             parsed.name,
		Description:      parsed.description,
		ShortDescription: parsed.shortDescription,
		ArgsHint:         parsed.argsHint,
		UserInvocable:    userInvocable,
		Triggers:         parsed.triggers,
		Tools:            parsed.tools,
		Instructions:     parsed.instructions,
	}
	if def.Name == "" {
		def.Name = name
	}
	return WriteSkillMarkdown(dir, def)
}
