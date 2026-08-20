package settings

import (
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// renderProviderCells draws one provider header as separately-aligned
// cells: name, active/selectable state, the API key's PRESENCE only -
// never its value (settings-screen.md §5; ProviderView.APIKeySet is a
// bool by construction, so there is nothing here that could leak one) -
// and its model count. render.Columns pads each cell to its column's
// widest value across every PROVIDER row (renderRows in models.go
// aligns providers and models as two separate groups, since the two
// kinds carry different columns).
func (s *modelsSection) renderProviderCells(p ports.ProviderView) []string {
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
	return []string{name, status, count}
}

// renderModelCells draws one model line under its provider as
// separately-aligned cells: name, context window, an optional
// reasoning-effort label, and whether it is the provider's active
// selection. The last two are appended only when present, which
// render.Columns handles as a ragged row.
func (s *modelsSection) renderModelCells(p ports.ProviderView, m ports.ModelView) []string {
	fg := render.Role(s.theme, s.tier, theme.RoleFG)
	name := "  " + fg.Render(m.Name)
	ctx := render.Role(s.theme, s.tier, theme.RoleFGSubtle).Render(fmt.Sprintf("%dk ctx", m.ContextWindowTokens/1000))
	cells := []string{name, ctx}
	if m.Reasoning != "" {
		cells = append(cells, render.Role(s.theme, s.tier, theme.RoleFGSubtle).Render(m.Reasoning))
	}
	if p.Active && p.ActiveModel == m.Name {
		cells = append(cells, render.Role(s.theme, s.tier, theme.RoleAccent).Render("active"))
	}
	return cells
}
