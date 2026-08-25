package demoharness

import (
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
