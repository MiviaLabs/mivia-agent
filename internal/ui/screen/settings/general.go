package settings

import (
	"context"
	"strconv"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/component/field"
	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/keymap"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// generalRow pairs one rendered field with the edit its current value
// produces. Every General setting is KindChoice - even scroll
// lines, as a short preset list - so the section needs no separate
// edit/commit mode: space (or enter) cycles the highlighted row AND
// applies it in the same key press, matching the plan's "space:
// toggle" for the common boolean case and extending it uniformly to
// every row rather than special-casing text input for one field
// (KindText stays reserved for a section that actually needs free
// text, e.g. Models' base_url in a later slice).
type generalRow struct {
	label string
	f     field.Model
	apply func(value string) ports.GeneralEdit
	// boolean marks a row whose value is one of "on"/"off". View renders
	// these as an explicit `[ ON  ]`/`[ OFF ]` control (RoleSuccess when
	// enabled, RoleFGMuted when disabled) instead of the field's bare
	// value text, so the state reads from the word itself - not from
	// colour alone, which stays readable in ASCII/NO_COLOR - while a
	// non-boolean row (scroll lines, approval default) keeps showing its
	// value directly, since "ON"/"OFF" would misdescribe a preset choice.
	boolean bool
}

// generalSection is the General settings section.
type generalSection struct {
	store         ports.GeneralSettings
	theme         theme.Theme
	tier          theme.Tier
	width, height int

	rows   []generalRow
	cursor int
	notice string
}

func newGeneralSection(store ports.GeneralSettings) *generalSection {
	return &generalSection{store: store}
}

func (s *generalSection) Title() string { return "General" }

func (s *generalSection) SetSize(w, h int) {
	s.width, s.height = w, h
	for i := range s.rows {
		s.rows[i].f.SetWidth(w)
	}
}

func (s *generalSection) SetTheme(t theme.Theme, tier theme.Tier) {
	s.theme, s.tier = t, tier
	for i := range s.rows {
		s.rows[i].f.SetTheme(t, tier)
	}
	if s.store != nil && len(s.rows) == 0 {
		s.rebuild()
	}
}

// boolChoice projects a bool onto the two-value "on"/"off" set every
// boolean row shares.
func boolChoice(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// scrollChoices is a short preset rather than free text: a scroll-lines
// value outside a sane range is a UX bug, not a setting anyone wants,
// so the field cannot represent one.
var scrollChoices = []string{"1", "2", "3", "5", "8"}

// boolRowField builds the field for a true/false boolean row from the
// given label and current view value. Centralising the KindChoice +
// on/off choices here keeps rebuild() focused on row composition, and
// keeps every boolean row visually consistent (the [ ON ]/[ OFF ]
// semantic control is rendered by valueCell from the row.boolean flag).
func boolRowField(s *generalSection, label string, on bool) field.Model {
	f := field.New(s.theme, s.tier, label, field.KindChoice, s.width)
	f.SetChoices([]string{"on", "off"}, boolChoice(on))
	return f
}

// rebuild (re)creates every row from the store's current values. It
// runs once at construction and again after every successful save, so
// a row that failed to apply still shows the last CONFIRMED value, not
// an optimistic local guess.
func (s *generalSection) rebuild() {
	v := s.store.General()

	// No theme row here: Ctrl-T already opens the dedicated theme
	// picker dialog (screen/themepicker), which live-previews every
	// theme as the cursor moves - a strictly better picking experience
	// than a KindChoice cycler, so General does not duplicate it.
	// ports.SetTheme/GeneralView.Theme stay - the port itself is not
	// removed, only this UI's use of it.
	mouseF := boolRowField(s, "mouse capture", v.Mouse)
	reasonF := boolRowField(s, "show reasoning", v.ShowReasoning)
	iterF := boolRowField(s, "iteration notice", v.ShowIterationNotices)
	cacheF := boolRowField(s, "prompt cache notice", v.ShowPromptCacheNotices)

	scrollF := field.New(s.theme, s.tier, "scroll lines", field.KindChoice, s.width)
	scrollF.SetChoices(scrollChoices, strconv.Itoa(v.ScrollLines))

	approvalF := field.New(s.theme, s.tier, "approval default", field.KindChoice, s.width)
	// Strength-ordered, and reachable in BOTH directions (see handleKey's
	// left/h binding). Every commit applies and persists immediately - this
	// section has no preview step - and the runtime half now fans out to
	// every pooled session, so the old "once, always, deny" order made
	// blanket auto-approval the only route from once to deny: it granted
	// it to every running session, backgrounded and worktree ones included,
	// and left an operator interrupted mid-cycle there durably. With the
	// values in strength order and a backward key, every keypress applies
	// exactly the neighbouring posture the operator moved toward, and no
	// route passes through one weaker than both of its endpoints.
	approvalF.SetChoices(approvalChoicesByStrength, v.ApprovalDefault)

	srF := boolRowField(s, "screen reader", v.ScreenReader)
	rmF := boolRowField(s, "reduced motion", v.ReducedMotion)

	// Persisted in the operator's USER config AND applied live to the
	// session's workspace root via the state re-arm - the never-silent
	// FULL DISK ACCESS notice is pushed into the transcript either way.
	fdF := boolRowField(s, "full disk access", v.FullDiskAccess)

	// Sync opt-out toggles for the [sync] table. Persisted to the same
	// workspace mivia.toml as every other general setting (not the user
	// config - see SetFullDiskAccess for the contrast). Takes effect on
	// the next session start - the live chatsync client is intentionally
	// not re-armed, matching the ScreenReader/ReducedMotion precedent.
	// Labels use the SAME word as the TOML key (include_thinking,
	// include_tool_io, stream_assistant) so an operator matching a row
	// to its setting has no translation step. (Earlier draft used
	// "include reasoning" which silently renamed the key for the user;
	// caught by architecture-review F2.)
	syncThinkingF := boolRowField(s, "sync: include thinking", v.SyncIncludeThinking)
	syncToolIOF := boolRowField(s, "sync: include tool io", v.SyncIncludeToolIO)
	syncStreamF := boolRowField(s, "sync: stream assistant", v.SyncStreamAssistant)

	s.rows = []generalRow{
		{"mouse capture", mouseF, func(val string) ports.GeneralEdit { return ports.SetMouse{On: val == "on"} }, true},
		{"show reasoning", reasonF, func(val string) ports.GeneralEdit { return ports.SetShowReasoning{On: val == "on"} }, true},
		{"iteration notice", iterF, func(val string) ports.GeneralEdit { return ports.SetShowIterationNotices{On: val == "on"} }, true},
		{"prompt cache notice", cacheF, func(val string) ports.GeneralEdit { return ports.SetShowPromptCacheNotices{On: val == "on"} }, true},
		{"scroll lines", scrollF, func(val string) ports.GeneralEdit {
			n, _ := strconv.Atoi(val) // val is always one of scrollChoices; Atoi cannot fail
			return ports.SetScrollLines{N: n}
		}, false},
		{"approval default", approvalF, func(val string) ports.GeneralEdit { return ports.SetApprovalDefault{Mode: val} }, false},
		{"screen reader", srF, func(val string) ports.GeneralEdit { return ports.SetScreenReader{On: val == "on"} }, true},
		{"reduced motion", rmF, func(val string) ports.GeneralEdit { return ports.SetReducedMotion{On: val == "on"} }, true},
		{"full disk access", fdF, func(val string) ports.GeneralEdit { return ports.SetFullDiskAccess{On: val == "on"} }, true},
		{"sync: include thinking", syncThinkingF, func(val string) ports.GeneralEdit { return ports.SetSyncIncludeThinking{On: val == "on"} }, true},
		{"sync: include tool io", syncToolIOF, func(val string) ports.GeneralEdit { return ports.SetSyncIncludeToolIO{On: val == "on"} }, true},
		{"sync: stream assistant", syncStreamF, func(val string) ports.GeneralEdit { return ports.SetSyncStreamAssistant{On: val == "on"} }, true},
	}
	if s.cursor >= len(s.rows) {
		s.cursor = len(s.rows) - 1
	}
}

// generalSavedMsg/generalFailedMsg are what awaitSave's Cmd yields once
// a SaveHandle finishes - the section's own small async result, kept
// local rather than routed through the Screen, since only this section
// cares about its own save outcome.
type generalSavedMsg struct{}
type generalFailedMsg struct{ message string }

// awaitSave blocks the returned Cmd on handle's channel until it
// closes, then reports the last event's outcome. A SaveHandle's
// contract guarantees a terminal Saved or Failed event before close,
// so the loop always has a last state to report.
func awaitSave(handle ports.SaveHandle) tea.Cmd {
	return func() tea.Msg {
		var last ports.SaveEvent
		for ev := range handle.Events() {
			last = ev
		}
		if last.State == ports.SaveFailed {
			return generalFailedMsg{message: last.Message}
		}
		if last.State != ports.SaveSaved {
			msg := last.Message
			if msg == "" {
				msg = "save incomplete"
			}
			return generalFailedMsg{message: msg}
		}
		return generalSavedMsg{}
	}
}

func (s *generalSection) Update(msg tea.Msg) (section, tea.Cmd) {
	switch msg := msg.(type) {
	case generalSavedMsg:
		s.notice = ""
		s.rebuild()
		return s, nil
	case generalFailedMsg:
		s.notice = msg.message
		// Re-show the last CONFIRMED values: the optimistic Cycle in
		// commit() left the row on the refused value, and this is the first
		// General row whose apply can legitimately fail by design (the
		// full-disk same-file refusal). Without the rebuild the row renders
		// "on" while the store holds false (bug-audit ec8a9ef4 a2-2).
		s.rebuild()
		return s, nil
	case tea.KeyPressMsg:
		return s.handleKey(msg)
	}
	return s, nil
}

func (s *generalSection) handleKey(msg tea.KeyPressMsg) (section, tea.Cmd) {
	if s.store == nil || len(s.rows) == 0 {
		return s, nil
	}
	switch msg.String() {
	case "up", "k":
		if s.cursor > 0 {
			s.cursor--
		}
	case "down", "j":
		if s.cursor < len(s.rows)-1 {
			s.cursor++
		}
	case "space", "enter":
		return s.commit(1)
	case "-":
		return s.commit(-1)
	}
	return s, nil
}

// commit cycles the highlighted row one step in delta's direction and
// applies that value immediately - see the type's own doc comment for why
// there is no separate preview step. delta is what lets a strength-ordered
// row (approval default) be tightened without first applying a weaker
// posture on the way.
func (s *generalSection) commit(delta int) (section, tea.Cmd) {
	row := &s.rows[s.cursor]
	row.f.Cycle(delta)
	edit := row.apply(row.f.Value())
	handle, err := s.store.Apply(context.Background(), ports.ScopeUser, edit)
	if err != nil {
		s.notice = err.Error()
		s.rebuild()
		return s, nil
	}
	return s, awaitSave(handle)
}

// approvalChoicesByStrength orders the approval postures from strongest to
// weakest. Anything that applies one of these values on every step must move
// through them in this order, so an operator never applies a posture weaker
// than both the one they left and the one they are heading for.
var approvalChoicesByStrength = []string{"deny", "once", "always"}

// boolControlText is the plain, unstyled text boolControl renders: an
// explicit `[ ON  ]`/`[ OFF ]` word rather than the bare "on"/"off"
// value text, so the state reads from the word itself - not from colour
// alone - which keeps it readable in ASCII/NO_COLOR tiers where
// RoleSuccess/RoleFGMuted resolve to no colour at all. Exposed
// separately from boolControl so call sites (tests, mainly) can find
// the rendered word without re-deriving the literal.
func boolControlText(on bool) string {
	if on {
		return "[ ON  ]"
	}
	return "[ OFF ]"
}

// boolControl renders a boolean row's semantic value control, styled
// with RoleSuccess when enabled and RoleFGMuted when disabled - colour
// as reinforcement on top of boolControlText's self-describing word,
// never as the only signal.
func boolControl(t theme.Theme, tier theme.Tier, on bool) string {
	if on {
		return render.Role(t, tier, theme.RoleSuccess).Render(boolControlText(true))
	}
	return render.Role(t, tier, theme.RoleFGMuted).Render(boolControlText(false))
}

// valueCell renders one row's value column: the semantic ON/OFF control
// for a boolean row, or the plain field value (scroll lines, approval
// default) for anything else - "[ ON  ]" would misdescribe a preset
// choice, so only boolean rows get the control treatment.
func (s *generalSection) valueCell(row generalRow) string {
	if row.boolean {
		return boolControl(s.theme, s.tier, row.f.Value() == "on")
	}
	return render.Role(s.theme, s.tier, theme.RoleFG).Render(row.f.Value())
}

func (s *generalSection) View() string {
	if s.store == nil {
		return render.Role(s.theme, s.tier, theme.RoleFGSubtle).Render("General is unavailable.")
	}
	avail := s.height
	if s.notice != "" && avail > 1 {
		avail--
	}
	start, end := render.WindowSlice(len(s.rows), s.cursor, avail)

	// Columns aligns every row's label/value pair together, over the
	// WHOLE row set rather than just the visible slice, so the column
	// widths (and therefore the value column's start position) stay
	// identical no matter which rows are currently scrolled into view -
	// scrolling must not shift the alignment the operator is reading by.
	cells := make([][]string, len(s.rows))
	for i, row := range s.rows {
		label := render.Role(s.theme, s.tier, theme.RoleFGSubtle).Render(row.label)
		cells[i] = []string{label, s.valueCell(row)}
	}
	aligned := render.Columns(2, cells)

	var b []byte
	for i := start; i < end; i++ {
		line := aligned[i]
		if i == s.cursor {
			line = "> " + line
		} else {
			line = "  " + line
		}
		b = append(b, line...)
		b = append(b, '\n')
	}
	if s.notice != "" {
		b = append(b, render.Role(s.theme, s.tier, theme.RoleWarning).Render(s.notice)...)
	}
	return string(b)
}

func (s *generalSection) Hints() []keymap.ID {
	return []keymap.ID{keymap.IDSettingsUp, keymap.IDSettingsDown, keymap.IDSettingsToggle, keymap.IDSettingsCycleBack}
}
