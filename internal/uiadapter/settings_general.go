package uiadapter

import (
	"context"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// settingsGeneral
type settingsGeneral struct{ *SettingsStore }

func (g settingsGeneral) General() ports.GeneralView {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.general
}

func (g settingsGeneral) Apply(_ context.Context, _ ports.Scope, e ports.GeneralEdit) (ports.SaveHandle, error) {
	return g.newSaveHandle(func() error { return g.applyGeneral(e) }), nil
}

// buildGeneralView seeds the General section's view from the resolved
// [tui] config, falling back to the same defaults initFromConfig used to
// hardcode inline - now field-by-field, so an operator's [tui] mouse,
// theme, show_reasoning, scroll_lines, screen_reader, or reduced_motion
// value is what actually renders at construction, not a stale literal.
func (s *SettingsStore) buildGeneralView(workspaceRoot string) ports.GeneralView {
	v := ports.GeneralView{
		Theme:           "mivia-dark",
		Mouse:           true,
		ShowReasoning:   true,
		ScrollLines:     3,
		ApprovalDefault: "always",
		// Sync* default to true because config/sync.go documents "absent
		// key == ON"; rendering false would lie to the operator about a
		// file that has no opinion on these switches. The resolved view
		// overrides these from s.res.Sync when it is non-nil below.
		SyncIncludeThinking: true,
		SyncIncludeToolIO:   true,
		SyncStreamAssistant: true,
	}
	if s.res != nil {
		if s.res.TUI.Theme != "" {
			v.Theme = s.res.TUI.Theme
		}
		if s.res.TUI.Mouse != nil {
			v.Mouse = *s.res.TUI.Mouse
		}
		if s.res.TUI.ShowReasoning != nil {
			v.ShowReasoning = *s.res.TUI.ShowReasoning
		}
		if s.res.TUI.ScrollLines != nil && *s.res.TUI.ScrollLines > 0 {
			v.ScrollLines = *s.res.TUI.ScrollLines
		}
		if s.res.TUI.ScreenReader != nil {
			v.ScreenReader = *s.res.TUI.ScreenReader
		}
		if s.res.TUI.ReducedMotion != nil {
			v.ReducedMotion = *s.res.TUI.ReducedMotion
		}
		v.ShowIterationNotices = s.res.ShowIterationNotices
		v.ShowPromptCacheNotices = s.res.ShowPromptCacheNotices
		v.ApprovalDefault = approvalModeToView(s.res.Approvals.ApprovalPolicy())
		// Sync fields mirror ResolvedSync (already collapsed from the
		// three-state file form by resolveSyncConfig).
		v.SyncIncludeThinking = s.res.Sync.IncludeThinking
		v.SyncIncludeToolIO = s.res.Sync.IncludeToolIO
		v.SyncStreamAssistant = s.res.Sync.StreamAssistant
	}
	// Full disk comes from the operator's user config, never from res: res
	// is the workspace-overlay-merged view, and this grant must only ever
	// reflect the operator's own file (config.UserFullDiskAccessForWorkspace
	// enforces that provenance and fails closed).
	v.FullDiskAccess = config.UserFullDiskAccessForWorkspace(workspaceRoot)
	return v
}

func generalViewToSettings(v ports.GeneralView) config.GeneralSettings {
	return config.GeneralSettings{
		Theme:                  v.Theme,
		Mouse:                  v.Mouse,
		ShowReasoning:          v.ShowReasoning,
		ShowIterationNotices:   v.ShowIterationNotices,
		ShowPromptCacheNotices: v.ShowPromptCacheNotices,
		ScrollLines:            v.ScrollLines,
		ApprovalDefault:        v.ApprovalDefault,
		ScreenReader:           v.ScreenReader,
		ReducedMotion:          v.ReducedMotion,
		// The three sync *bool fields are intentionally left nil here:
		// a whole-view projection would materialise sync keys into the
		// file whenever any general edit fires, breaking the
		// absent-means-on contract. syncEditToSettings, called from
		// applyGeneral, populates exactly the pointer the operator
		// actually touched.
	}
}

// syncEditToSettings projects a single sync-related GeneralEdit onto the
// GeneralSettings that UpdateGeneralConfig consumes. Only the pointer
// matching the edit is populated; all three are nil for every other
// GeneralEdit, which is what makes the [sync] upsert in UpdateGeneralConfig
// a write-only-the-touched-key operation rather than a wholesale rewrite.
//
// Defined as a separate helper rather than inlined in applyGeneral so the
// rule "never write a sync key from a non-sync edit" is testable in
// isolation (see TestGeneralViewToSettings_LeavesSyncPointersNil's negative
// counterpart in settings_general_test.go).
func syncEditToSettings(e ports.GeneralEdit, gs *config.GeneralSettings) {
	switch v := e.(type) {
	case ports.SetSyncIncludeThinking:
		on := v.On
		gs.SyncIncludeThinking = &on
	case ports.SetSyncIncludeToolIO:
		on := v.On
		gs.SyncIncludeToolIO = &on
	case ports.SetSyncStreamAssistant:
		on := v.On
		gs.SyncStreamAssistant = &on
	}
}

// applySetFullDiskAccess persists the full-disk grant to the operator's
// USER config only - never the generic UpdateGeneralConfig path, whose
// configPath() may resolve to the workspace's own committable
// .mivia/mivia.toml (audit F2) - and then re-arms the LIVE session root
// so the change lands without a restart (AR-4's sanctioned synchronized
// re-arm; Root.SetUnrestricted is atomic). A live lift is always
// announced through fullDiskNotifier with the single-sourced disclosure
// line - lifting confinement is never silent, live or at launch.
func (s *SettingsStore) applySetFullDiskAccess(on bool) error {
	workspaceRoot := ""
	if s.agentState != nil {
		workspaceRoot = s.agentState.WorkspaceRoot
	}
	if err := config.SetUserFullDiskAccess(workspaceRoot, on); err != nil {
		return fmt.Errorf("persist full-disk setting: %w", err)
	}
	s.general.FullDiskAccess = on
	if s.agentState.ApplyFullDisk(on) {
		text := config.FullDiskNoticeText
		if !on {
			text = "workspace: full disk access disabled — file tools are confined to the workspace again"
		}
		if fn := s.fullDiskNotifier; fn != nil {
			go fn(text)
		}
	}
	return nil
}

// applyApprovalDefault records the operator's approval posture and applies it
// immediately, so "accept always" (and "deny") take effect without a restart -
// the runtime half of the setting; persistence is UpdateGeneralConfig's. Every
// POOLED session is re-armed, not just the focused one: an operator tightening
// the gate means it everywhere, exactly as the full-disk toggle fans out.
func (s *SettingsStore) applyApprovalDefault(mode string) {
	s.general.ApprovalDefault = mode
	if s.res != nil {
		s.res.Approvals.DefaultMode = mode
	}
	if s.pool != nil {
		s.pool.ApplyApprovalDefault(mode)
		return
	}
	if s.sess != nil {
		s.sess.SetApprovalPolicy(config.NormalizeDefaultMode(mode))
	}
}

// generalPriorState is the last CONFIRMED (pre-mutation) snapshot
// applyGeneral takes before mutating s.general/s.res, so a failed
// persist can restore exactly what it changed rather than guessing.
type generalPriorState struct {
	general         ports.GeneralView
	showIter        bool
	showCache       bool
	approvalDefault string
}

func (s *SettingsStore) snapshotGeneralPrior() generalPriorState {
	p := generalPriorState{general: s.general}
	if s.res != nil {
		p.showIter = s.res.ShowIterationNotices
		p.showCache = s.res.ShowPromptCacheNotices
		p.approvalDefault = s.res.Approvals.DefaultMode
	}
	return p
}

// applyGeneral is always called with s.mu already held by the caller
// (newSaveHandle's apply() wrapper takes the lock for the whole call,
// same as every other apply* method in this file) - it must NOT lock
// s.mu itself, or it deadlocks on Go's non-reentrant sync.Mutex. Every
// field read/write below is safe unguarded for exactly that reason.
func (s *SettingsStore) applyGeneral(e ports.GeneralEdit) error {
	prior := s.snapshotGeneralPrior()

	mouseNotifier, err := s.mutateGeneral(e, prior)
	if err != nil {
		return err
	}

	if cfgPath := s.configPath(); cfgPath != "" {
		settings := generalViewToSettings(s.general)
		// syncEditToSettings sets exactly one sync pointer for a sync
		// edit, none otherwise; this is the only path that materialises
		// [sync] keys into the file, so an unrelated general edit cannot
		// stamp include_thinking = true into a config that left it
		// absent at default.
		syncEditToSettings(e, &settings)
		if err := config.UpdateGeneralConfig(cfgPath, settings); err != nil {
			s.rollbackGeneral(prior)
			return fmt.Errorf("persist general settings: %w", err)
		}
	}
	if mouseNotifier != nil {
		go mouseNotifier(s.general.Mouse)
	}
	return nil
}

// mutateGeneral applies one edit to s.general/s.res/live runtime state
// and returns the mouse notifier to fire (after a successful persist)
// when the edit was SetMouse. It never touches disk - persistGeneral
// (via applyGeneral) does that.
func (s *SettingsStore) mutateGeneral(e ports.GeneralEdit, prior generalPriorState) (func(bool), error) {
	var mouseNotifier func(bool)
	switch v := e.(type) {
	case ports.SetTheme:
		s.general.Theme = v.Name
	case ports.SetMouse:
		s.general.Mouse = v.On
		mouseNotifier = s.mouseNotifier // fired by the caller, after the persist
	case ports.SetShowReasoning:
		s.general.ShowReasoning = v.On
		if s.conv != nil {
			s.conv.SetShowReasoning(v.On)
		}
	case ports.SetShowIterationNotices:
		s.general.ShowIterationNotices = v.On
		if s.res != nil {
			s.res.ShowIterationNotices = v.On
		}
		if s.conv != nil {
			s.conv.SetNoticeOptions(TranslateOptions{
				ShowIterationNotices:   v.On,
				ShowPromptCacheNotices: prior.general.ShowPromptCacheNotices,
			})
		}
	case ports.SetShowPromptCacheNotices:
		s.general.ShowPromptCacheNotices = v.On
		if s.res != nil {
			s.res.ShowPromptCacheNotices = v.On
		}
		if s.conv != nil {
			s.conv.SetNoticeOptions(TranslateOptions{
				ShowIterationNotices:   prior.general.ShowIterationNotices,
				ShowPromptCacheNotices: v.On,
			})
		}
	case ports.SetScrollLines:
		if v.N <= 0 {
			return nil, fmt.Errorf("scroll lines must be positive")
		}
		s.general.ScrollLines = v.N
		if s.conv != nil {
			s.conv.SetScrollLines(v.N)
		}
	case ports.SetApprovalDefault:
		s.applyApprovalDefault(v.Mode)
	case ports.SetScreenReader:
		s.general.ScreenReader = v.On
	case ports.SetReducedMotion:
		s.general.ReducedMotion = v.On
	case ports.SetSyncIncludeThinking:
		// Matches the SetScreenReader/SetReducedMotion precedent: set the
		// view field, do not mirror into s.res (the live chatsync client
		// is intentionally not re-armed; takes effect on next session
		// start). prior.general is a whole-struct copy captured before
		// mutateGeneral ran, so rollback restores this field via the
		// existing prior.general = ... assignment in rollbackGeneral
		// without any new generalPriorState plumbing.
		s.general.SyncIncludeThinking = v.On
	case ports.SetSyncIncludeToolIO:
		s.general.SyncIncludeToolIO = v.On
	case ports.SetSyncStreamAssistant:
		s.general.SyncStreamAssistant = v.On
	case ports.SetFullDiskAccess:
		// USER-config-only persistence, restart-to-apply - see
		// applySetFullDiskAccess for the provenance rules (audit F2/AR-4).
		return nil, s.applySetFullDiskAccess(v.On)
	default:
		return nil, fmt.Errorf("unknown general edit %T", e)
	}
	return mouseNotifier, nil
}

// rollbackGeneral restores s.general, the resolved config, and every
// live runtime side effect mutateGeneral applied, to the state prior
// captured before the edit - called when persistence to disk fails, so
// the in-memory store and the running session never hold a value that
// never reached disk.
func (s *SettingsStore) rollbackGeneral(prior generalPriorState) {
	s.general = prior.general
	if s.res != nil {
		s.res.ShowIterationNotices = prior.showIter
		s.res.ShowPromptCacheNotices = prior.showCache
		s.res.Approvals.DefaultMode = prior.approvalDefault
	}
	if s.conv != nil {
		s.conv.SetShowReasoning(prior.general.ShowReasoning)
		s.conv.SetNoticeOptions(TranslateOptions{
			ShowIterationNotices:   prior.general.ShowIterationNotices,
			ShowPromptCacheNotices: prior.general.ShowPromptCacheNotices,
		})
		s.conv.SetScrollLines(prior.general.ScrollLines)
	}
	if s.pool != nil {
		s.pool.ApplyApprovalDefault(prior.general.ApprovalDefault)
	} else if s.sess != nil {
		s.sess.SetApprovalPolicy(config.NormalizeDefaultMode(prior.general.ApprovalDefault))
	}
}
