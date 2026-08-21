package cli

import (
	"strings"
)

func (m *tuiModel) skillSlashTurn(input string) (sent, display string, ok bool) {
	spec, ok := m.skillSlashSpec(input)
	if !ok {
		return "", "", false
	}
	return renderSkillSlashPrompt(spec.definition.Instructions, spec.args), spec.display, true
}

func (m *tuiModel) skillSlashSpec(input string) (skillSlashSpec, bool) {
	fields := strings.Fields(input)
	if len(fields) == 0 {
		return skillSlashSpec{}, false
	}
	binding := m.session.CurrentBinding()
	command, found := findSlashCommand(fields[0], slashSurfaceTUI, binding.SkillRegistry)
	if !found || command.Kind != slashKindSkill {
		return skillSlashSpec{}, false
	}
	definition, found := binding.SkillRegistry.Get(command.SkillName)
	if !found {
		return skillSlashSpec{}, false
	}
	normalizedInput := strings.TrimSpace(input)
	args := strings.TrimSpace(strings.TrimPrefix(normalizedInput, fields[0]))
	return skillSlashSpec{definition: definition, args: args, display: "⚙ " + normalizedInput}, true
}
