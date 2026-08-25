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
	// A Global row for a provider that HAS a project override is
	// shadowed at runtime by that override (see ProviderView's doc
	// comment on EffectiveDefaultModel) - flag it here so the row does
	// not read as "this IS the effective default" next to the Project
	// group's row for the same provider making the same claim.
	if p.Scope == ports.ScopeUser && p.HasProjectOverride {
		status += "  " + render.Role(s.theme, s.tier, theme.RoleFGSubtle).Render("overridden by project")
	}
	count := render.Role(s.theme, s.tier, theme.RoleFGSubtle).Render(fmt.Sprintf("%d models", len(p.Models)))
	return []string{name, status, count}
}

// renderModelCells draws one model line under its provider as
// separately-aligned cells: name, context window, an optional
// reasoning-effort label, and whether it is active/default.
func (s *modelsSection) renderModelCells(p ports.ProviderView, m ports.ModelView) []string {
	fg := render.Role(s.theme, s.tier, theme.RoleFG)
	name := "  " + fg.Render(m.Name)
	ctx := render.Role(s.theme, s.tier, theme.RoleFGSubtle).Render(fmt.Sprintf("%dk ctx", m.ContextWindowTokens/1000))
	cells := []string{name, ctx}
	if m.Reasoning != "" {
		cells = append(cells, render.Role(s.theme, s.tier, theme.RoleFGSubtle).Render(m.Reasoning))
	}
	isActive := p.Active && p.ActiveModel == m.Name
	isDefault := p.DefaultModel == m.Name
	// A Global default is shadowed once the project scope overrides
	// this provider (see the "overridden by project" status flag in
	// renderProviderCells) - label it accordingly so this row does not
	// claim to be the effective default when it is not.
	defaultLabel := "default"
	if p.Scope == ports.ScopeUser && p.HasProjectOverride {
		defaultLabel = "default here (shadowed)"
	}
	switch {
	case isActive && isDefault:
		cells = append(cells, render.Role(s.theme, s.tier, theme.RoleAccent).Render("active, "+defaultLabel))
	case isActive:
		cells = append(cells, render.Role(s.theme, s.tier, theme.RoleAccent).Render("active"))
	case isDefault:
		cells = append(cells, render.Role(s.theme, s.tier, theme.RoleSuccess).Render(defaultLabel))
	}
	return cells
}
