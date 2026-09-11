package settings

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/component/field"
	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/keymap"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// runHistoryLimit is how many past runs the detail area shows per
// automation. A fixed small window, not the whole history: the section
// has no scroll of its own yet, so an unbounded list would run off the
// bottom of the pane with no way back.
const runHistoryLimit = 5

// automationsSection is the Automations settings section: the only
// section with a second-level detail (schedule, trigger, run history)
// and a streaming live-run view. Creating a new automation needs the
// same kind of multi-field entry Models' provider creation does and is
// the same documented gap: "n" reports a notice, not a silent no-op.
type automationsSection struct {
	store         ports.AutomationSettings
	theme         theme.Theme
	tier          theme.Tier
	width, height int

	rows   []ports.Automation
	cursor int
	notice string

	// runs is the highlighted automation's recent history, refreshed on
	// every cursor move so browsing needs no trigger first.
	runs []ports.Run

	// watch is the open live-run subscription for watchID, non-nil only
	// while a manually-triggered run's Pending/Running transitions are
	// still expected. liveRun is the last event it delivered.
	watch   ports.RunHandle
	watchID string
	liveRun *ports.Run

	// Editing / Add Form State. There is no confirm-delete step for
	// automations (unlike mcp/agents), so this is simpler than those
	// sections' equivalent fields.
	editing      bool
	isNew        bool
	formFields   []field.Model
	formFocus    int
	editOriginal ports.Automation
}

func newAutomationsSection(store ports.AutomationSettings) *automationsSection {
	return &automationsSection{store: store}
}

func (s *automationsSection) Title() string { return "Automations" }

// CapturingInput reports true while the create/edit form is open, so
// settings.go's handleKey routes every key straight to this section's
// own Update instead of letting ContextSettings bindings (which would
// steal letters like "n" or "e" the form needs as literal text) match
// first - the same gate mcpSection.CapturingInput and
// agentsSection.CapturingInput already provide.
func (s *automationsSection) CapturingInput() bool { return s.editing }

func (s *automationsSection) SetSize(w, h int) { s.width, s.height = w, h }

func (s *automationsSection) SetTheme(t theme.Theme, tier theme.Tier) {
	s.theme, s.tier = t, tier
	if s.store != nil && s.rows == nil {
		s.rebuild()
	}
}

func (s *automationsSection) rebuild() {
	s.rows = s.store.Automations()
	if s.cursor >= len(s.rows) {
		s.cursor = len(s.rows) - 1
	}
	if s.cursor < 0 {
		s.cursor = 0
	}
	s.refreshRuns()
}

func (s *automationsSection) refreshRuns() {
	if len(s.rows) == 0 {
		s.runs = nil
		return
	}
	s.runs = s.store.Runs(s.rows[s.cursor].ID, runHistoryLimit)
}

type automationsSavedMsg struct{}

// automationsFailedMsg carries a save failure. awaitAutomationsSave is
// shared by toggleEnabled, remove, trigger, cancelRun, and the editor's
// saveEditor, but only trigger and cancelRun legitimately target the
// currently armed watch (they are the only operations that arm or act
// on it). cancelsWatch distinguishes those two call sites from the
// others so Update does not tear down an unrelated automation's live
// watch on an unrelated failed save (cursor navigation only clears
// s.liveRun, never s.watch, so the watch can still belong to a
// different row than the one that just failed to save).
type automationsFailedMsg struct {
	message      string
	cancelsWatch bool
}
type automationsRunMsg struct{ run ports.Run }

// automationsWatchEndedMsg carries the handle whose channel closed, so
// Update can tell a superseded watch's teardown (e.g. Cancel() called
// by a later trigger or by automationsFailedMsg) from the CURRENT
// watch's own end: only the latter should clear s.watch/s.watchID.
type automationsWatchEndedMsg struct{ handle ports.RunHandle }

// awaitAutomationsSave waits for handle to resolve. cancelsWatch is
// threaded through to the resulting automationsFailedMsg on failure: pass
// true only from the call sites that legitimately own the currently
// armed watch (trigger, cancelRun); every other call site passes false so
// an unrelated automation's failed save cannot tear down this row's live
// watch.
func awaitAutomationsSave(handle ports.SaveHandle, cancelsWatch bool) tea.Cmd {
	return func() tea.Msg {
		var last ports.SaveEvent
		for ev := range handle.Events() {
			last = ev
		}
		if last.State == ports.SaveFailed {
			return automationsFailedMsg{message: last.Message, cancelsWatch: cancelsWatch}
		}
		return automationsSavedMsg{}
	}
}

// watchNext reads one run off handle's channel. The section re-arms
// this itself after each Pending/Running delivery (see Update); it
// does not loop internally, so the section - not this function - owns
// when watching stops.
func watchNext(handle ports.RunHandle) tea.Cmd {
	return func() tea.Msg {
		run, ok := <-handle.Events()
		if !ok {
			return automationsWatchEndedMsg{handle: handle}
		}
		return automationsRunMsg{run: run}
	}
}

func (s *automationsSection) Update(msg tea.Msg) (section, tea.Cmd) {
	switch msg := msg.(type) {
	case automationsSavedMsg:
		s.notice = ""
		s.rebuild()
		return s, nil
	case automationsFailedMsg:
		s.notice = msg.message
		if msg.cancelsWatch {
			if s.watch != nil {
				s.watch.Cancel()
				s.watch, s.watchID = nil, ""
			}
			// A cancelsWatch failure means the backend rejected the
			// action the user's last-seen liveRun snapshot was based on
			// (e.g. cancelRun lost a race against the run reaching a
			// terminal state) - that snapshot is now stale. Clear it and
			// reconcile against the store's authoritative view, same as
			// handleRunUpdate's own terminal-state branch does.
			s.liveRun = nil
			s.rebuild()
		}
		return s, nil
	case automationsRunMsg:
		return s.handleRunUpdate(msg.run)
	case automationsWatchEndedMsg:
		// Only the CURRENT watch's own end clears state; a superseded
		// watch's teardown (already replaced by a later trigger, or
		// cancelled by automationsFailedMsg above) arrives here too and
		// must be a no-op, not a clobber of the watch armed after it.
		if msg.handle == s.watch {
			s.watch, s.watchID = nil, ""
		}
		return s, nil
	case tea.KeyPressMsg:
		return s.handleKey(msg)
	}
	return s, nil
}

// handleRunUpdate applies one streamed run to liveRun, refreshes the
// cached history once the run reaches a terminal state (so the new row
// appears without waiting for the next cursor move), and re-arms the
// watch only while the run can still change state - a terminal run
// will not fire again, and re-arming past that would leak a
// goroutine blocked forever on a channel that will not send again
// until the NEXT trigger.
func (s *automationsSection) handleRunUpdate(run ports.Run) (section, tea.Cmd) {
	s.liveRun = &run
	if run.State != ports.RunPending && run.State != ports.RunRunning {
		if s.watch != nil {
			s.watch.Cancel()
			s.watch, s.watchID = nil, ""
		}
		s.rebuild()
		return s, nil
	}
	if s.watch == nil {
		return s, nil
	}
	return s, watchNext(s.watch)
}

func (s *automationsSection) handleKey(msg tea.KeyPressMsg) (section, tea.Cmd) {
	// The editor's own key handling must run BEFORE the empty-list guard
	// below: that guard used to swallow every key including "n" once the
	// list was empty, which meant a fresh workspace's Automations card
	// (no automations defined yet) could never open the "new automation"
	// form at all - the reported bug this change fixes.
	if s.editing {
		return s.handleEditorKey(msg)
	}
	if s.store == nil || len(s.rows) == 0 {
		if msg.String() == "n" {
			s.openEditor(ports.Automation{Enabled: true, Trigger: ports.TriggerSpec{Kind: ports.TriggerManual}}, true)
		}
		return s, nil
	}
	switch msg.String() {
	case "up", "k":
		if s.cursor > 0 {
			s.cursor--
			s.liveRun = nil
			s.refreshRuns()
		}
		s.notice = ""
	case "down", "j":
		if s.cursor < len(s.rows)-1 {
			s.cursor++
			s.liveRun = nil
			s.refreshRuns()
		}
		s.notice = ""
	case "space":
		return s.toggleEnabled()
	case "t":
		return s.trigger()
	case "s":
		return s.cancelRun()
	case "x":
		return s.remove()
	case "n":
		s.openEditor(ports.Automation{Enabled: true, Trigger: ports.TriggerSpec{Kind: ports.TriggerManual}}, true)
	case "enter", "e":
		if s.editorRepresentable(s.rows[s.cursor]) {
			s.openEditor(s.rows[s.cursor], false)
		} else {
			s.notice = "editing this automation is not available in this build yet (multi-step action or cron/at-times schedule)"
		}
	}
	return s, nil
}

func (s *automationsSection) toggleEnabled() (section, tea.Cmd) {
	row := s.rows[s.cursor]
	handle, err := s.store.Apply(context.Background(), ports.ScopeUser, ports.SetAutomationEnabled{ID: row.ID, On: !row.Enabled})
	if err != nil {
		s.notice = err.Error()
		return s, nil
	}
	return s, awaitAutomationsSave(handle, false)
}

func (s *automationsSection) remove() (section, tea.Cmd) {
	handle, err := s.store.Apply(context.Background(), ports.ScopeUser, ports.RemoveAutomation{ID: s.rows[s.cursor].ID})
	if err != nil {
		s.notice = err.Error()
		return s, nil
	}
	return s, awaitAutomationsSave(handle, false)
}

// trigger fires a manual run and switches to watching it. Watch opens
// BEFORE Apply so the watcher is registered before the fake's async
// goroutine can publish the run's first (Pending) state - opening it
// after would risk missing that first event to a race between this
// goroutine and the one Apply starts.
func (s *automationsSection) trigger() (section, tea.Cmd) {
	row := s.rows[s.cursor]
	if s.watch != nil {
		s.watch.Cancel()
	}
	watch, err := s.store.Watch(context.Background(), row.ID)
	if err != nil {
		s.notice = err.Error()
		return s, nil
	}
	s.watch, s.watchID, s.liveRun = watch, row.ID, nil

	handle, err := s.store.Apply(context.Background(), ports.ScopeUser, ports.TriggerAutomation{ID: row.ID})
	if err != nil {
		s.notice = err.Error()
		return s, nil
	}
	return s, tea.Batch(awaitAutomationsSave(handle, true), watchNext(s.watch))
}

// cancelRun stops the run currently shown as live, if any. A no-op
// when there is no live run or it has already reached a terminal
// state - there is nothing left for the backend to cancel.
func (s *automationsSection) cancelRun() (section, tea.Cmd) {
	if s.liveRun == nil || (s.liveRun.State != ports.RunPending && s.liveRun.State != ports.RunRunning) {
		return s, nil
	}
	handle, err := s.store.Apply(context.Background(), ports.ScopeUser, ports.CancelAutomationRun{RunID: s.liveRun.ID})
	if err != nil {
		s.notice = err.Error()
		return s, nil
	}
	return s, awaitAutomationsSave(handle, true)
}

func (s *automationsSection) View() string {
	if s.store == nil {
		return render.Role(s.theme, s.tier, theme.RoleFGSubtle).Render("Automations is unavailable.")
	}
	if s.editing {
		return s.renderEditor()
	}
	cells := make([][]string, len(s.rows))
	for i, row := range s.rows {
		cells[i] = s.renderCells(row)
	}
	aligned := render.Columns(rowGap, cells)

	avail := s.height
	if s.notice != "" && avail > 1 {
		avail--
	}
	start, end := render.WindowSlice(len(aligned), s.cursor, avail)

	var b []byte
	for i, line := range aligned[start:end] {
		actualIdx := start + i
		marker := "  "
		if actualIdx == s.cursor {
			marker = "> "
		}
		b = append(b, (marker + line)...)
		b = append(b, '\n')
	}
	if len(s.rows) > 0 {
		b = append(b, '\n')
		b = append(b, s.renderDetail(s.rows[s.cursor])...)
	}
	if s.notice != "" {
		b = append(b, '\n')
		b = append(b, render.Role(s.theme, s.tier, theme.RoleWarning).Render(s.notice)...)
	}
	return string(b)
}

func (s *automationsSection) Hints() []keymap.ID {
	if s.editing {
		return []keymap.ID{
			keymap.IDSettingsUp,
			keymap.IDSettingsDown,
			keymap.IDSettingsToggle,
			keymap.IDSettingsBack,
		}
	}
	return []keymap.ID{keymap.IDSettingsUp, keymap.IDSettingsDown, keymap.IDSettingsToggle, keymap.IDSettingsTrigger, keymap.IDSettingsCancelRun, keymap.IDSettingsNew, keymap.IDSettingsDelete}
}
