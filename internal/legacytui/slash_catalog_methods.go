package legacytui

import (
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
)

// SkillSlashSpec holds skill slash spec state. Relocated from
// internal/cli/slash_catalog.go: unused elsewhere in that package, and this
// package needs unexported field access (definition/args/display) that
// cross-package reach cannot provide.
type SkillSlashSpec struct {
	definition skills.Definition
	args       string
	display    string
}

func (m *TUIModel) skillSlashTurn(input string) (sent, display string, ok bool) {
	spec, ok := m.skillSlashSpec(input)
	if !ok {
		return "", "", false
	}
	return cli.RenderSkillSlashPrompt(spec.definition.Instructions, spec.args), spec.display, true
}

func (m *TUIModel) skillSlashSpec(input string) (SkillSlashSpec, bool) {
	fields := strings.Fields(input)
	if len(fields) == 0 {
		return SkillSlashSpec{}, false
	}
	binding := m.session.CurrentBinding()
	command, found := cli.FindSlashCommand(fields[0], cli.SlashSurfaceTUI, binding.SkillRegistry)
	if !found || command.Kind != cli.SlashKindSkill {
		return SkillSlashSpec{}, false
	}
	definition, found := binding.SkillRegistry.Get(command.SkillName)
	if !found {
		return SkillSlashSpec{}, false
	}
	normalizedInput := strings.TrimSpace(input)
	args := strings.TrimSpace(strings.TrimPrefix(normalizedInput, fields[0]))
	return SkillSlashSpec{definition: definition, args: args, display: "⚙ " + normalizedInput}, true
}
