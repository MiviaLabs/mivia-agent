package demoharness

import (
	"context"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// harnessSkills is the ports.SkillSettings adapter.
type harnessSkills struct{ *Harness }

func (s harnessSkills) Skills() []ports.SkillView {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ports.SkillView, len(s.settingsSkills))
	copy(out, s.settingsSkills)
	return out
}

func (s harnessSkills) Apply(_ context.Context, _ ports.Scope, e ports.SkillEdit) (ports.SaveHandle, error) {
	return s.newSaveHandle(func() error { return s.applySkill(e) }), nil
}

func (h *Harness) findSkill(name string) int {
	for i := range h.settingsSkills {
		if h.settingsSkills[i].Name == name {
			return i
		}
	}
	return -1
}

func (h *Harness) applySkill(e ports.SkillEdit) error {
	switch v := e.(type) {
	case ports.RemoveSkill:
		i := h.findSkill(v.Name)
		if i < 0 {
			return fmt.Errorf("skill %q not found", v.Name)
		}
		h.settingsSkills = append(h.settingsSkills[:i], h.settingsSkills[i+1:]...)
	case ports.SetSkillUserInvocable:
		i := h.findSkill(v.Name)
		if i < 0 {
			return fmt.Errorf("skill %q not found", v.Name)
		}
		h.settingsSkills[i].UserInvocable = v.On
	case ports.SaveSkill:
		i := h.findSkill(v.Name)
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
		if i >= 0 {
			h.settingsSkills[i] = skill
		} else {
			h.settingsSkills = append(h.settingsSkills, skill)
		}
	default:
		return fmt.Errorf("unknown skill edit %T", e)
	}
	return nil
}
