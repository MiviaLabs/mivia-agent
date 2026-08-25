package uiadapter

import (
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

func (s *SettingsStore) skillRegistry() *skills.Registry {
	if s == nil {
		return nil
	}
	if s.agentState != nil && s.agentState.SkillRegFull != nil {
		return s.agentState.SkillRegFull
	}
	if s.sess != nil {
		return s.sess.CurrentBinding().SkillRegistry
	}
	return nil
}

func (s *SettingsStore) initSkillsFromConfig() {
	reg := s.skillRegistry()
	if reg == nil {
		return
	}
	for _, sk := range reg.List() {
		s.skills = append(s.skills, ports.SkillView{
			Name:              sk.Name,
			Description:       sk.Description,
			Origin:            string(sk.Origin),
			Scope:             sk.Scope,
			Tools:             sk.Tools,
			ArgsHint:          sk.ArgsHint,
			UserInvocable:     sk.UserInvocable,
			Triggers:          sk.Triggers,
			InstructionsChars: len(sk.Instructions),
		})
	}
}

// settingsSkills implements ports.SkillSettings.
type settingsSkills struct{ *SettingsStore }

func (sk settingsSkills) Skills() []ports.SkillView {
	sk.mu.Lock()
	defer sk.mu.Unlock()
	if reg := sk.skillRegistry(); reg != nil {
		var out []ports.SkillView
		for _, s := range reg.List() {
			out = append(out, ports.SkillView{
				Name:              s.Name,
				Description:       s.Description,
				Origin:            string(s.Origin),
				Scope:             s.Scope,
				Tools:             s.Tools,
				ArgsHint:          s.ArgsHint,
				UserInvocable:     s.UserInvocable,
				Triggers:          s.Triggers,
				InstructionsChars: len(s.Instructions),
			})
		}
		return out
	}
	out := make([]ports.SkillView, len(sk.skills))
	copy(out, sk.skills)
	return out
}
