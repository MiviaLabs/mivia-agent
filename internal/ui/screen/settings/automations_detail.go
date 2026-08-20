package settings

import (
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// renderRow draws one automation's list line: name, enabled/disabled,
// trigger kind, and last run's state if any.
func (s *automationsSection) renderRow(a ports.Automation) string {
	fg := render.Role(s.theme, s.tier, theme.RoleFG)
	name := fg.Bold(true).Render(a.Name)

	enabled := render.Role(s.theme, s.tier, theme.RoleFGSubtle).Render("disabled")
	if a.Enabled {
		enabled = render.Role(s.theme, s.tier, theme.RoleSuccess).Render("enabled")
	}
	trig := render.Role(s.theme, s.tier, theme.RoleFGSubtle).Render(triggerLabel(a.Trigger))

	last := ""
	if a.LastRun != nil {
		last = "  " + render.Role(s.theme, s.tier, runStateRole(a.LastRun.State)).Render(runStateLabel(a.LastRun.State))
	}
	return name + "  " + enabled + "  " + trig + last
}

// renderDetail draws the highlighted automation's expanded panel:
// description, trigger detail, a live run if one is streaming, and
// recent history otherwise.
func (s *automationsSection) renderDetail(a ports.Automation) []byte {
	subtle := render.Role(s.theme, s.tier, theme.RoleFGSubtle)
	var out []byte
	if a.Description != "" {
		out = append(out, subtle.Render(a.Description)...)
		out = append(out, '\n')
	}
	out = append(out, subtle.Render("trigger: "+triggerDetail(a.Trigger))...)
	out = append(out, '\n')

	if s.watch != nil && s.watchID == a.ID && s.liveRun != nil {
		out = append(out, s.renderLiveRun(*s.liveRun)...)
		return out
	}
	if len(s.runs) == 0 {
		out = append(out, subtle.Render("no runs yet")...)
		return out
	}
	out = append(out, subtle.Render("recent runs:")...)
	out = append(out, '\n')
	for _, r := range s.runs {
		out = append(out, s.renderRunLine(r)...)
		out = append(out, '\n')
	}
	return out
}

func (s *automationsSection) renderLiveRun(r ports.Run) []byte {
	role := runStateRole(r.State)
	line := render.Role(s.theme, s.tier, role).Render("watching: " + runStateLabel(r.State))
	return []byte(line)
}

func (s *automationsSection) renderRunLine(r ports.Run) []byte {
	role := runStateRole(r.State)
	line := "  " + render.Role(s.theme, s.tier, role).Render(runStateLabel(r.State)) +
		"  " + render.Role(s.theme, s.tier, theme.RoleFGSubtle).Render(r.StartedAt.Format("2006-01-02 15:04:05"))
	if r.State == ports.RunFailed && r.Message != "" {
		line += "  " + render.Role(s.theme, s.tier, theme.RoleDanger).Render(r.Message)
	}
	return []byte(line)
}

func triggerLabel(t ports.TriggerSpec) string {
	if t.Kind == ports.TriggerManual {
		return "manual"
	}
	return "scheduled"
}

// triggerDetail describes a schedule in the terms the fake/adapter
// actually carries: interval, a fixed set of times, or cron text plus
// its (mandatory) timezone - never a computed next-fire guess this
// package cannot make on its own.
func triggerDetail(t ports.TriggerSpec) string {
	if t.Kind == ports.TriggerManual || t.Schedule == nil {
		return "manual"
	}
	sched := t.Schedule
	switch sched.Kind {
	case ports.ScheduleInterval:
		return fmt.Sprintf("every %s", sched.Every)
	case ports.ScheduleAt:
		return fmt.Sprintf("%d scheduled time(s)", len(sched.At))
	case ports.ScheduleRecurring:
		return fmt.Sprintf("%s (%s)", sched.Cron, sched.TZ)
	default:
		return "scheduled"
	}
}

func runStateLabel(st ports.RunState) string {
	switch st {
	case ports.RunPending:
		return "pending"
	case ports.RunRunning:
		return "running"
	case ports.RunSucceeded:
		return "succeeded"
	case ports.RunFailed:
		return "failed"
	case ports.RunCancelled:
		return "cancelled"
	default:
		return "unknown"
	}
}

func runStateRole(st ports.RunState) theme.Role {
	switch st {
	case ports.RunSucceeded:
		return theme.RoleSuccess
	case ports.RunFailed:
		return theme.RoleDanger
	case ports.RunRunning, ports.RunPending:
		return theme.RoleInfo
	default:
		return theme.RoleFGSubtle
	}
}
