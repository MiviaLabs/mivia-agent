package uiadapter

import (
	"context"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
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
		sources := []skills.Source{
			{Dir: workspace.UserSkillsDir(), Origin: skills.OriginUser},
			{Dir: workspace.SkillsDir(""), Origin: skills.OriginProject},
		}
		reg, _, _ = skills.LoadMarkdownSources(sources, skills.LoadOptions{})
	}
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
			Instructions:      sk.Instructions,
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
				Instructions:      s.Instructions,
			})
		}
		return out
	}
	out := make([]ports.SkillView, len(sk.skills))
	copy(out, sk.skills)
	return out
}

func (sk settingsSkills) Apply(_ context.Context, scope ports.Scope, e ports.SkillEdit) (ports.SaveHandle, error) {
	return sk.newSaveHandle(func() error { return sk.applySkill(scope, e) }), nil
}

func (s *SettingsStore) findSkill(name string) int {
	for i := range s.skills {
		if s.skills[i].Name == name {
			return i
		}
	}
	return -1
}

func (s *SettingsStore) skillsDirectory(origin string, scope ports.Scope) string {
	if origin == "user" || scope == ports.ScopeUser {
		return workspace.UserSkillsDir()
	}
	return workspace.SkillsDir("")
}

func (s *SettingsStore) applySkill(scope ports.Scope, e ports.SkillEdit) error {
	switch v := e.(type) {
	case ports.RemoveSkill:
		dir := s.skillsDirectory(v.Origin, scope)
		if err := skills.RemoveSkillDirectory(dir, v.Name); err != nil {
			return err
		}
		if i := s.findSkill(v.Name); i >= 0 {
			s.skills = append(s.skills[:i], s.skills[i+1:]...)
		}
		s.reloadSkillRegistry()
	case ports.SetSkillUserInvocable:
		dir := s.skillsDirectory(v.Origin, scope)
		if err := skills.UpdateSkillUserInvocable(dir, v.Name, v.On); err != nil {
			return err
		}
		if i := s.findSkill(v.Name); i >= 0 {
			s.skills[i].UserInvocable = v.On
		}
		s.reloadSkillRegistry()
	case ports.SaveSkill:
		dir := s.skillsDirectory(v.Origin, scope)
		def := skills.Definition{
			Name:          v.Name,
			Description:   v.Description,
			UserInvocable: v.UserInvocable,
			Tools:         v.Tools,
			Triggers:      v.Triggers,
			Instructions:  v.Instructions,
		}
		if err := skills.WriteSkillMarkdown(dir, def); err != nil {
			return err
		}
		skill := ports.SkillView{
			Name:              v.Name,
			Description:       v.Description,
			Origin:            v.Origin,
			UserInvocable:     v.UserInvocable,
			Tools:             v.Tools,
			Triggers:          v.Triggers,
			Instructions:      v.Instructions,
			InstructionsChars: len(v.Instructions),
		}
		if i := s.findSkill(v.Name); i >= 0 {
			s.skills[i] = skill
		} else {
			s.skills = append(s.skills, skill)
		}
		s.reloadSkillRegistry()
	default:
		return fmt.Errorf("unknown skill edit %T", e)
	}
	return nil
}

func (s *SettingsStore) reloadSkillRegistry() {
	userDir := workspace.UserSkillsDir()
	projDir := workspace.SkillsDir("")
	sources := []skills.Source{
		{Dir: userDir, Origin: skills.OriginUser},
		{Dir: projDir, Origin: skills.OriginProject},
	}
	reg, _, err := skills.LoadMarkdownSources(sources, skills.LoadOptions{})
	if err == nil && reg != nil {
		if s.agentState != nil {
			s.agentState.SkillRegFull = reg
		}
	}
}
