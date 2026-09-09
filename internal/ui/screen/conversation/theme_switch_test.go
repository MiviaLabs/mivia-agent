package conversation

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/picker"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/replay"
)

func TestThemeChangedMsgUpdatesTransientComponents(t *testing.T) {
	dark, light, themes := themePair(t)
	s := New(dark, theme.TierASCII, themes, replay.New(nil, 0), nil, 40, fixedNow)

	modelPicker := picker.New(dark, theme.TierASCII, []string{"model"})
	agentPicker := picker.New(dark, theme.TierASCII, []string{"agent"})
	sessionPicker := newSessionPicker(dark, theme.TierASCII, nil)
	palettePicker := picker.New(dark, theme.TierASCII, []string{"command"})
	effortPicker := picker.New(dark, theme.TierASCII, []string{"high"})
	login := newLoginDialog(dark, theme.TierASCII)
	s.modelPicker = &modelPicker
	s.agentPicker = &agentPicker
	s.sessionPicker = &sessionPicker
	s.palettePicker = &palettePicker
	s.effortPicker = &effortPicker
	s.login = &login

	next, _ := s.Update(app.ThemeChangedMsg{Theme: light, Tier: theme.TierTrueColor})
	got := next.(Screen)
	for name, th := range map[string]theme.Theme{
		"model picker":   got.modelPicker.Theme,
		"agent picker":   got.agentPicker.Theme,
		"session picker": got.sessionPicker.Theme,
		"palette picker": got.palettePicker.Theme,
		"effort picker":  got.effortPicker.Theme,
		"login dialog":   got.login.Theme,
		"login email":    got.login.email.Theme,
		"login password": got.login.password.Theme,
	} {
		if th.Name != light.Name {
			t.Errorf("%s theme = %q, want %q", name, th.Name, light.Name)
		}
	}
	if got.login.email.Tier != theme.TierTrueColor || got.login.password.Tier != theme.TierTrueColor {
		t.Errorf("login fields kept stale tier: email=%v password=%v", got.login.email.Tier, got.login.password.Tier)
	}
}
