package settings

import (
	"context"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/component/field"
	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// automationFormBase is the field-index offset between the new-automation
// form (which carries a leading ID text field) and the edit form (which
// does not, because an existing automation's ID is immutable through this
// editor): 0 for a new automation, -1 for an edit. Every field after ID
// shifts by this amount rather than by two hand-maintained index sets, so
// the new/edit field lists can never drift out of sync with each other.
func (s *automationsSection) automationFormBase() int {
	if s.isNew {
		return 0
	}
	return -1
}

// automationFormIndices names every field's slot in s.formFields, given
// the current isNew/edit form shape. idIdx is meaningful only when isNew.
// unattendedIdx is always the LAST field, appended after Prompt/Skill, so
// its index is always the highest and every existing index above stays
// unchanged.
func (s *automationsSection) automationFormIndices() (idIdx, nameIdx, descIdx, enabledIdx, triggerIdx, everyIdx, actionIdx, promptIdx, unattendedIdx int) {
	base := s.automationFormBase()
	return 0, 1 + base, 2 + base, 3 + base, 4 + base, 5 + base, 6 + base, 7 + base, 8 + base
}

// openEditor opens the create/edit form over a, the automation as it
// exists right now (a blank Automation{Enabled:true, Trigger:TriggerManual}
// for "new"). a is stashed into editOriginal so saveEditor's non-form
// fields (Worktree, LastRun, NextFire, Scope, and - for an edit - ID) carry
// forward untouched rather than being silently reset to zero values.
func (s *automationsSection) openEditor(a ports.Automation, isNew bool) {
	s.editing = true
	s.isNew = isNew
	s.editOriginal = a
	s.notice = ""

	fieldWidth := s.width - 16
	if fieldWidth < 20 {
		fieldWidth = 40
	}

	mkChoice := func(label string, choices []string, active string) field.Model {
		f := field.New(s.theme, s.tier, label, field.KindChoice, fieldWidth)
		f.SetChoices(choices, active)
		return f
	}
	mkText := func(label string, val string) field.Model {
		f := field.New(s.theme, s.tier, label, field.KindText, fieldWidth)
		f.SetValue(val)
		return f
	}

	enabledChoice := "off"
	if a.Enabled {
		enabledChoice = "on"
	}

	triggerChoice := "manual"
	everyVal := ""
	if a.Trigger.Kind == ports.TriggerScheduled && a.Trigger.Schedule != nil && a.Trigger.Schedule.Kind == ports.ScheduleInterval {
		triggerChoice = "every"
		everyVal = a.Trigger.Schedule.Every.String()
	}

	actionChoice := "prompt"
	promptVal := ""
	if len(a.Action.Steps) == 1 {
		switch step := a.Action.Steps[0]; step.Kind {
		case ports.ActionStepSkill:
			actionChoice = "skill"
			promptVal = step.Ref
		case ports.ActionStepPrompt:
			actionChoice = "prompt"
			promptVal = step.Prompt
		}
	}

	// unattendedChoice defaults to "deny" for a new automation (isNew=true
	// has no editOriginal.Unattended to prefill from - a.Unattended on the
	// blank Automation{} passed to openEditor is already the ports zero
	// value, UnattendedPolicyDeny, so this branch is really just naming
	// that default explicitly rather than relying on the zero value by
	// accident). Editing an existing automation prefills from its real
	// current value.
	unattendedChoice := "deny"
	if a.Unattended == ports.UnattendedPolicyAuto {
		unattendedChoice = "auto"
	}

	var fields []field.Model
	if isNew {
		fields = append(fields, mkText("ID:           ", a.ID))
	}
	fields = append(fields,
		mkText("Name:         ", a.Name),
		mkText("Description:  ", a.Description),
		mkChoice("Enabled:      ", []string{"on", "off"}, enabledChoice),
		mkChoice("Trigger:      ", []string{"manual", "every"}, triggerChoice),
		mkText("Every:        ", everyVal),
		mkChoice("Action:       ", []string{"prompt", "skill"}, actionChoice),
		mkText("Prompt/Skill: ", promptVal),
		mkChoice("Unattended:   ", []string{"deny", "auto"}, unattendedChoice),
	)
	s.formFields = fields
	s.formFocus = 0
	s.updateFormFieldFocus()
}

func (s *automationsSection) updateFormFieldFocus() tea.Cmd {
	var cmd tea.Cmd
	for i := range s.formFields {
		if i == s.formFocus {
			cmd = s.formFields[i].Focus()
		} else {
			s.formFields[i].Blur()
		}
	}
	return cmd
}

func (s *automationsSection) handleEditorKey(msg tea.KeyPressMsg) (section, tea.Cmd) {
	switch msg.String() {
	case "esc":
		s.editing = false
		s.notice = ""
		return s, nil
	case "ctrl+s":
		return s.saveEditor()
	case "tab":
		s.formFocus = (s.formFocus + 1) % len(s.formFields)
		cmd := s.updateFormFieldFocus()
		return s, cmd
	case "shift+tab":
		s.formFocus = (s.formFocus - 1 + len(s.formFields)) % len(s.formFields)
		cmd := s.updateFormFieldFocus()
		return s, cmd
	}

	if s.formFocus < 0 || s.formFocus >= len(s.formFields) {
		return s, nil
	}

	_, _, _, enabledIdx, triggerIdx, _, actionIdx, _, unattendedIdx := s.automationFormIndices()
	isChoice := s.formFocus == enabledIdx || s.formFocus == triggerIdx || s.formFocus == actionIdx || s.formFocus == unattendedIdx
	if isChoice {
		switch msg.String() {
		case " ", "space", "enter", "right", "l":
			s.formFields[s.formFocus].Cycle(1)
		case "left", "h":
			s.formFields[s.formFocus].Cycle(-1)
		}
		return s, nil
	}

	var cmd tea.Cmd
	s.formFields[s.formFocus], cmd = s.formFields[s.formFocus].Update(msg)
	return s, cmd
}

// editorRepresentable reports whether a can be opened in this MVP form:
// at most a single Prompt or Skill action step (or none at all - a
// zero-step automation, including every freshly-created one, is
// representable as a blank prompt step), no bare Workflow compat alias,
// and - when scheduled - only an interval ("every N") schedule. Anything
// else (multi-step actions, agent/slash/workflow steps, cron or
// fixed-times schedules) has no form field to hold it and must be
// refused rather than silently truncated on save.
func (s *automationsSection) editorRepresentable(a ports.Automation) bool {
	switch len(a.Action.Steps) {
	case 0:
		if a.Action.Workflow != "" {
			return false
		}
	case 1:
		switch a.Action.Steps[0].Kind {
		case ports.ActionStepPrompt, ports.ActionStepSkill:
		default:
			return false
		}
	default:
		return false
	}
	if a.Trigger.Kind == ports.TriggerScheduled {
		if a.Trigger.Schedule == nil || a.Trigger.Schedule.Kind != ports.ScheduleInterval {
			return false
		}
	}
	return true
}

// automationFromForm builds the Automation to save from the current form
// values, layered over editOriginal so every field this form has no
// control for (Worktree, LastRun, NextFire, Scope, and - on an edit - ID)
// survives untouched. A non-empty second return means the form is
// invalid; the caller must not call Apply.
func (s *automationsSection) automationFromForm() (ports.Automation, string) {
	idIdx, nameIdx, descIdx, enabledIdx, triggerIdx, everyIdx, actionIdx, promptIdx, unattendedIdx := s.automationFormIndices()

	result := s.editOriginal

	if s.isNew {
		id := strings.TrimSpace(s.formFields[idIdx].Value())
		if id == "" {
			return ports.Automation{}, "an automation id is required"
		}
		result.ID = id
	}

	result.Name = s.formFields[nameIdx].Value()
	result.Description = s.formFields[descIdx].Value()
	result.Enabled = s.formFields[enabledIdx].Value() == "on"

	switch s.formFields[triggerIdx].Value() {
	case "every":
		d, err := time.ParseDuration(strings.TrimSpace(s.formFields[everyIdx].Value()))
		if err != nil {
			return ports.Automation{}, "Every must be a duration like 30m or 2h"
		}
		if d <= 0 {
			return ports.Automation{}, "Every must be positive"
		}
		// The on-disk wire format is whole seconds (automation.portsTriggerToSpec
		// truncates via int64(Every / time.Second)), so a sub-second duration
		// like "500ms" would silently save as every_seconds=0 - a broken
		// schedule NextFire rejects and the scheduler's deadlineMap never
		// arms, leaving an enabled automation that can never fire with no
		// error surfaced at save time. Reject before that truncation can
		// happen, not after.
		if d.Truncate(time.Second) != d || d < time.Second {
			return ports.Automation{}, "Every must be a whole number of seconds, at least 1s"
		}
		tz := ""
		if s.editOriginal.Trigger.Kind == ports.TriggerScheduled && s.editOriginal.Trigger.Schedule != nil && s.editOriginal.Trigger.Schedule.Kind == ports.ScheduleInterval {
			tz = s.editOriginal.Trigger.Schedule.TZ
		}
		result.Trigger = ports.TriggerSpec{Kind: ports.TriggerScheduled, Schedule: &ports.ScheduleSpec{
			Kind: ports.ScheduleInterval, Every: d, TZ: tz,
		}}
	default:
		result.Trigger = ports.TriggerSpec{Kind: ports.TriggerManual}
	}

	promptVal := s.formFields[promptIdx].Value()
	switch s.formFields[actionIdx].Value() {
	case "skill":
		result.Action = ports.ActionRef{Steps: []ports.ActionStep{{Kind: ports.ActionStepSkill, Ref: promptVal}}}
	default:
		result.Action = ports.ActionRef{Steps: []ports.ActionStep{{Kind: ports.ActionStepPrompt, Prompt: promptVal}}}
	}

	switch s.formFields[unattendedIdx].Value() {
	case "auto":
		result.Unattended = ports.UnattendedPolicyAuto
	default:
		result.Unattended = ports.UnattendedPolicyDeny
	}

	return result, ""
}

func (s *automationsSection) saveEditor() (section, tea.Cmd) {
	result, errMsg := s.automationFromForm()
	if errMsg != "" {
		s.notice = errMsg
		return s, nil
	}
	handle, err := s.store.Apply(context.Background(), ports.ScopeUser, ports.UpsertAutomation{Automation: result})
	if err != nil {
		s.notice = err.Error()
		return s, nil
	}
	s.editing = false
	return s, awaitAutomationsSave(handle)
}

func (s *automationsSection) renderEditor() string {
	accent := render.Role(s.theme, s.tier, theme.RoleAccent)
	subtle := render.Role(s.theme, s.tier, theme.RoleFGSubtle)

	title := "Add New Automation"
	if !s.isNew {
		title = "Edit Automation: " + s.editOriginal.ID
	}

	var lines []string
	lines = append(lines, accent.Bold(true).Render(title))
	lines = append(lines, "")

	for _, f := range s.formFields {
		lines = append(lines, "  "+f.View())
	}

	_, _, _, _, _, _, _, _, unattendedIdx := s.automationFormIndices()
	if unattendedIdx >= 0 && unattendedIdx < len(s.formFields) && s.formFields[unattendedIdx].Value() == "auto" {
		lines = append(lines, "  "+subtle.Render("caution: unattended tool calls will be auto-approved for this automation"))
	}

	lines = append(lines, "")
	if s.notice != "" {
		lines = append(lines, "  "+render.Role(s.theme, s.tier, theme.RoleDanger).Render(s.notice))
		lines = append(lines, "")
	}

	hint := "ctrl+s save  esc cancel"
	lines = append(lines, "  "+subtle.Render(hint))

	return strings.Join(lines, "\n")
}
