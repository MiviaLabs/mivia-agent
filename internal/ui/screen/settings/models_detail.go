package settings

import (
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// renderProviderRow draws one provider header: name, active/selectable
// state, the API key's PRESENCE only - never its value
// (settings-screen.md §5; ProviderView.APIKeySet is a bool by
// construction, so there is nothing here that could leak one).
func (s *modelsSection) renderProviderRow(p ports.ProviderView) string {
	fg := render.Role(s.theme, s.tier, theme.RoleFG)
	name := fg.Bold(true).Render(p.Name)

	status := render.Role(s.theme, s.tier, theme.RoleFGSubtle).Render("key missing")
	if p.APIKeyEnv == "" {
		status = render.Role(s.theme, s.tier, theme.RoleFGSubtle).Render("loopback")
	} else if p.APIKeySet {
		status = render.Role(s.theme, s.tier, theme.RoleSuccess).Render("key set")
	}
	if p.Active {
		status = render.Role(s.theme, s.tier, theme.RoleAccent).Render("active") + "  " + status
	}
	if !p.Selectable && p.DisabledReason != "" {
		status += "  " + render.Role(s.theme, s.tier, theme.RoleDanger).Render(p.DisabledReason)
	}
	count := render.Role(s.theme, s.tier, theme.RoleFGSubtle).Render(fmt.Sprintf("%d models", len(p.Models)))
	return name + "  " + status + "  " + count
}

// renderModelRow draws one model line under its provider: name,
// context window, and whether it is the provider's active selection.
func (s *modelsSection) renderModelRow(p ports.ProviderView, m ports.ModelView) string {
	fg := render.Role(s.theme, s.tier, theme.RoleFG)
	name := "  " + fg.Render(m.Name)
	ctx := render.Role(s.theme, s.tier, theme.RoleFGSubtle).Render(fmt.Sprintf("%dk ctx", m.ContextWindowTokens/1000))
	line := name + "  " + ctx
	if m.Reasoning != "" {
		line += "  " + render.Role(s.theme, s.tier, theme.RoleFGSubtle).Render(m.Reasoning)
	}
	if p.Active && p.ActiveModel == m.Name {
		line += "  " + render.Role(s.theme, s.tier, theme.RoleAccent).Render("active")
	}
	return line
}
