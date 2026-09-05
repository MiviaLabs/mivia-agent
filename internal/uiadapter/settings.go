package uiadapter

import (
	"context"
	"fmt"
	"net/url"
	"sync"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// SettingsStore holds the active configuration and state for settings management.
type SettingsStore struct {
	sess       *chat.Session
	conv       *Conversation
	res        *config.Resolved
	agentState *cliagents.AgentSessionState
	// pool fans operator-wide runtime settings out to every live session.
	// Nil for a settings store built without one (tests, one-shot commands),
	// which then falls back to the active session alone.
	pool *SessionPool

	mu sync.Mutex

	general     ports.GeneralView
	project     ports.ProjectView
	providers   []ports.ProviderView
	mcp         []ports.MCPServerView
	agents      []ports.AgentView
	automations []ports.Automation
	skills      []ports.SkillView
	runs        map[string][]ports.Run
	watchers    map[string][]chan ports.Run

	// mouseNotifier, when set by the launcher, receives every live
	// "mouse capture" change so the running program can flip its mouse
	// mode without a restart. Called outside the store lock.
	mouseNotifier func(on bool)

	// fullDiskNotifier, when set by the launcher, receives the disclosure
	// text for every LIVE full-disk re-arm (lift or re-impose) so the
	// conversation transcript can carry the never-silent notice. Called
	// outside the store lock; only fires when a re-arm was actually wired.
	fullDiskNotifier func(text string)

	saveSeq uint64
}

// NewSettingsStore builds a SettingsStore populated from the resolved configuration and agent state.
func NewSettingsStore(sess *chat.Session, res *config.Resolved, state *cliagents.AgentSessionState) *SettingsStore {
	s := &SettingsStore{
		sess:       sess,
		res:        res,
		agentState: state,
		runs:       make(map[string][]ports.Run),
		watchers:   make(map[string][]chan ports.Run),
	}
	s.initFromConfig()
	return s
}

// SetActiveSession updates the active session pointer for SettingsStore.
func (s *SettingsStore) SetActiveSession(sess *chat.Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sess = sess
}

// SetConversation attaches the active Conversation to receive live notice option updates.
func (s *SettingsStore) SetConversation(conv *Conversation) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conv = conv
	if s.conv != nil {
		s.conv.SetNoticeOptions(TranslateOptions{
			ShowIterationNotices:   s.general.ShowIterationNotices,
			ShowPromptCacheNotices: s.general.ShowPromptCacheNotices,
		})
	}
}

// SetMouseNotifier registers the callback that receives every live
// "mouse capture" change. The launcher wires it to the running program;
// nil clears it. Safe to call before or after construction.
// SetMouseNotifier wires the launcher-side bridge for live mouse-capture
// flips (see wireMouseNotifier). Mirrored by SetFullDiskNotifier.
func (s *SettingsStore) SetMouseNotifier(fn func(on bool)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mouseNotifier = fn
}

// SetFullDiskNotifier wires the launcher-side bridge that carries the
// never-silent full-disk disclosure into the conversation transcript when
// the operator's Settings -> General toggle re-arms the live root (see
// wireFullDiskNotifier). Mirrors SetMouseNotifier; nil clears it.
func (s *SettingsStore) SetFullDiskNotifier(fn func(text string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fullDiskNotifier = fn
}

func (s *SettingsStore) initFromConfig() {
	showIter := false
	showCache := false
	approvalDefault := "always"
	fullDisk := false
	workspaceRoot := ""
	if s.agentState != nil {
		workspaceRoot = s.agentState.WorkspaceRoot
	}
	if s.res != nil {
		showIter = s.res.ShowIterationNotices
		showCache = s.res.ShowPromptCacheNotices
		approvalDefault = approvalModeToView(s.res.Approvals.ApprovalPolicy())
	}
	// Full disk comes from the operator's user config, never from res: res
	// is the workspace-overlay-merged view, and this grant must only ever
	// reflect the operator's own file (config.UserFullDiskAccessForWorkspace
	// enforces that provenance and fails closed).
	fullDisk = config.UserFullDiskAccessForWorkspace(workspaceRoot)
	s.general = ports.GeneralView{
		Theme:                  "mivia-dark",
		Mouse:                  true,
		ShowReasoning:          true,
		ShowIterationNotices:   showIter,
		ShowPromptCacheNotices: showCache,
		ScrollLines:            3,
		ApprovalDefault:        approvalDefault,
		ScreenReader:           false,
		ReducedMotion:          false,
		FullDiskAccess:         fullDisk,
	}
	s.initProjectsFromConfig()
	s.initProvidersFromConfig()
	s.initAgentsFromConfig()
	s.initSkillsFromConfig()
	s.initMCPFromConfig()
}

// approvalModeToView (settings_approval.go) maps a resolved
// config.ApprovalPolicy* value back to the settings-screen choice.

func (s *SettingsStore) initProvidersFromConfig() {
	s.providers = s.buildProviderViews()
}

// projectConfigPath resolves the workspace's own .mivia/mivia.toml path
// from agentState.WorkspaceRoot, or "" when there is no workspace (a nil
// agentState, or one with an empty WorkspaceRoot - the classic REPL/
// one-shot path and any test harness that never wired one). Mirrors how
// settingsSkills.skillsDirectory and agentsDirForScope resolve their own
// project-scope directories from the same field.
func (s *SettingsStore) projectConfigPath() string {
	if s.agentState == nil {
		return ""
	}
	return config.ProjectConfigPath(s.agentState.WorkspaceRoot)
}

// providerConfigPathForScope resolves which file a provider default-model
// edit at scope should land in: ScopeUser is the base config
// (s.configPath(), same file every other provider edit already targets),
// ScopeProject is the workspace's own .mivia/mivia.toml. Returns "" when
// that file is not resolvable (no workspace) or is literally the same
// file as the base config - a project layer that IS the base file has no
// separate "project override" to write, the same guard loadFile's own
// workspaceOverlayConfigPath applies before treating a workspace file as
// an overlay.
func (s *SettingsStore) providerConfigPathForScope(scope ports.Scope) string {
	if scope == ports.ScopeUser {
		return s.configPath()
	}
	projectPath := s.projectConfigPath()
	if projectPath == "" || projectPath == s.configPath() {
		return ""
	}
	return projectPath
}

// buildProviderViews reads the resolved model catalog (Selectable,
// Active, Models, context windows - everything NOT scope-split) plus
// each scope's OWN unmerged default_model keys (config.
// LoadProviderDefaultOverrides against the base config file and,
// separately, the project file - see providerConfigPathForScope for why
// they must be read unmerged rather than off s.res.ModelCatalog()'s
// already-overlay-merged ProviderModelGroup.DefaultModel) and returns
// one ScopeUser row per catalog provider plus one additional ScopeProject
// row for every provider that has its own project override. Called at
// construction and again after every default-model edit (see
// applySetDefaultModel and friends) rather than patching fields in
// place, because a default-model edit can both change a value AND
// change which scopes exist for a provider (setting a project override
// where none existed adds a row; clearing the last one removes it) -
// harder to keep consistent by hand than by re-deriving from the two
// small on-disk reads. Falls back through mergeUntrackedProviders (see
// its own doc comment) at every return point so a provider that lives
// only in s.providers - never in s.res's own catalog - is not silently
// dropped by this re-derive.
func (s *SettingsStore) buildProviderViews() []ports.ProviderView {
	if s.res == nil {
		return nil
	}
	catalog := s.res.ModelCatalog()
	baseOverrides, projectOverrides := s.loadProviderDefaultOverrides()
	if len(catalog) == 0 && s.res.ProviderName != "" {
		fresh := []ports.ProviderView{s.buildFallbackProviderView()}
		return mergeUntrackedProviders(s.providers, fresh, baseOverrides, projectOverrides)
	}

	var out []ports.ProviderView
	for _, g := range catalog {
		out = append(out, s.buildProviderRows(g, baseOverrides, projectOverrides)...)
	}
	return mergeUntrackedProviders(s.providers, out, baseOverrides, projectOverrides)
}

// loadProviderDefaultOverrides reads the two scopes' own unmerged
// default_model keys (base config file, then the project file when one
// exists and differs from the base - see providerConfigPathForScope).
// Shared by both buildProviderViews branches so the fallback (single
// legacy provider, no catalog) and the normal catalog path read the
// exact same two files the same way.
func (s *SettingsStore) loadProviderDefaultOverrides() (base, project map[string]string) {
	base, _ = config.LoadProviderDefaultOverrides(s.configPath())
	if projectPath := s.providerConfigPathForScope(ports.ScopeProject); projectPath != "" {
		project, _ = config.LoadProviderDefaultOverrides(projectPath)
	}
	return base, project
}

// buildFallbackProviderView builds the single ScopeUser row used when
// s.res has no model catalog but does have a legacy single-provider
// config (ProviderName plus Model/Models/ModelProfiles).
func (s *SettingsStore) buildFallbackProviderView() ports.ProviderView {
	var models []ports.ModelView
	switch {
	case len(s.res.ModelProfiles) > 0:
		for _, p := range s.res.ModelProfiles {
			models = append(models, ports.ModelView{Name: p.Name, ContextWindowTokens: p.ContextWindowTokens})
		}
	case len(s.res.Models) > 0:
		for _, m := range s.res.Models {
			models = append(models, ports.ModelView{Name: m, ContextWindowTokens: 128000})
		}
	case s.res.Model != "":
		models = append(models, ports.ModelView{Name: s.res.Model, ContextWindowTokens: 128000})
	}
	activeModel := s.res.Model
	if s.sess != nil && s.sess.CurrentSelection().Model != "" {
		activeModel = s.sess.CurrentSelection().Model
	}
	return ports.ProviderView{
		Name:                  s.res.ProviderName,
		Active:                true,
		Selectable:            true,
		Models:                models,
		ActiveModel:           activeModel,
		DefaultModel:          s.res.Model,
		Scope:                 ports.ScopeUser,
		EffectiveDefaultModel: s.res.Model,
	}
}

// buildModelViews renders g.Models (context window, output ceiling,
// reasoning efforts) into the secret-free ports.ModelView shape shared
// by every row buildProviderRows returns for g.
func buildModelViews(g config.ProviderModelGroup) []ports.ModelView {
	var models []ports.ModelView
	for _, m := range g.Models {
		var efforts []string
		for _, eff := range m.ReasoningEfforts {
			efforts = append(efforts, string(eff))
		}
		models = append(models, ports.ModelView{
			Name:                m.Name,
			ContextWindowTokens: m.ContextWindowTokens,
			MaxOutputTokens:     m.MaxOutputTokens,
			ReasoningEfforts:    efforts,
			Reasoning:           string(m.Reasoning),
		})
	}
	return models
}

// buildProviderRows renders one catalog group g into one ScopeUser row
// plus, when the project scope has its own non-empty override for g,
// a second ScopeProject row - see buildProviderViews' own doc comment
// for why both scopes are separate rows rather than one merged view.
func (s *SettingsStore) buildProviderRows(g config.ProviderModelGroup, baseOverrides, projectOverrides map[string]string) []ports.ProviderView {
	models := buildModelViews(g)
	var activeModel string
	active := g.Active
	if s.sess != nil && s.sess.CurrentSelection().ProviderName != "" {
		active = (s.sess.CurrentSelection().ProviderName == g.Provider)
		if active {
			activeModel = s.sess.CurrentSelection().Model
		}
	}
	if activeModel == "" {
		for _, m := range g.Models {
			if active && activeModel == "" {
				activeModel = m.Name
			}
		}
	}
	globalDefault := baseOverrides[g.Provider]
	if globalDefault == "" && len(g.Models) > 0 {
		globalDefault = g.Models[0].Name
	}
	effectiveDefault := globalDefault
	projectDefault, hasOverride := projectOverrides[g.Provider]
	if hasOverride && projectDefault != "" {
		effectiveDefault = projectDefault
	}
	rows := []ports.ProviderView{{
		Name:                  g.Provider,
		Active:                active,
		Selectable:            g.Selectable,
		DisabledReason:        g.DisabledReason,
		Models:                models,
		ActiveModel:           activeModel,
		DefaultModel:          globalDefault,
		Scope:                 ports.ScopeUser,
		HasProjectOverride:    hasOverride && projectDefault != "",
		EffectiveDefaultModel: effectiveDefault,
	}}
	if hasOverride && projectDefault != "" {
		rows = append(rows, ports.ProviderView{
			Name:                  g.Provider,
			Active:                active,
			Selectable:            g.Selectable,
			DisabledReason:        g.DisabledReason,
			Models:                models,
			ActiveModel:           activeModel,
			DefaultModel:          projectDefault,
			Scope:                 ports.ScopeProject,
			HasProjectOverride:    true,
			EffectiveDefaultModel: effectiveDefault,
		})
	}
	return rows
}

// mergeUntrackedProviders appends every entry of existing whose Name does
// not appear anywhere in fresh, preserving it as-is EXCEPT for its own
// default-model fields, which are re-derived from baseOverrides/
// projectOverrides (the same two unmerged reads buildProviderViews just
// took for every catalog provider) rather than trusted as-is - existing
// is s.providers as it stood BEFORE this rebuild, so its DefaultModel/
// EffectiveDefaultModel/HasProjectOverride are a stale snapshot from
// whenever that row was last built, and a default-model edit landing
// via mergeUntrackedProviders (a provider never in s.res's own catalog)
// would otherwise appear to silently no-op: it writes the override file
// via config.UpdateProviderDefaultModel just fine, then this merge
// undoes the visible result by resurrecting the OLD row untouched. Only
// name/base/models/active state are carried over unchanged; either
// override map may be nil (buildProviderViews' single-provider fallback
// branch has none loaded), in which case the row's default fields
// simply fall back to "" the same way a brand-new catalog provider with
// no default_model key would.
func mergeUntrackedProviders(existing, fresh []ports.ProviderView, baseOverrides, projectOverrides map[string]string) []ports.ProviderView {
	known := make(map[string]bool, len(fresh))
	for _, p := range fresh {
		known[p.Name] = true
	}
	for _, p := range existing {
		if known[p.Name] {
			continue
		}
		globalDefault := baseOverrides[p.Name]
		effectiveDefault := globalDefault
		projectDefault, hasOverride := projectOverrides[p.Name]
		if hasOverride && projectDefault != "" {
			effectiveDefault = projectDefault
		}
		p.DefaultModel = globalDefault
		p.HasProjectOverride = hasOverride && projectDefault != ""
		p.EffectiveDefaultModel = effectiveDefault
		p.Scope = ports.ScopeUser
		fresh = append(fresh, p)
		known[p.Name] = true
		if hasOverride && projectDefault != "" {
			proj := p
			proj.DefaultModel = projectDefault
			proj.Scope = ports.ScopeProject
			fresh = append(fresh, proj)
		}
	}
	return fresh
}

// initAgentsFromConfig is implemented in settings_agents.go

// Settings returns the ports.Settings bundle with all section adapters.
func (s *SettingsStore) Settings() ports.Settings {
	return ports.Settings{
		General:     settingsGeneral{s},
		Projects:    settingsProjects{s},
		Providers:   settingsProviders{s},
		MCP:         settingsMCP{s},
		Agents:      settingsAgents{s},
		Automations: settingsAutomations{s},
		Skills:      settingsSkills{s},
	}
}

type saveHandle struct {
	id     string
	events chan ports.SaveEvent
	cancel func()
}

func (h *saveHandle) ID() string                     { return h.id }
func (h *saveHandle) Events() <-chan ports.SaveEvent { return h.events }
func (h *saveHandle) Cancel()                        { h.cancel() }

func (s *SettingsStore) newSaveHandle(apply func() error) ports.SaveHandle {
	s.mu.Lock()
	s.saveSeq++
	id := fmt.Sprintf("save-%d", s.saveSeq)
	s.mu.Unlock()

	ch := make(chan ports.SaveEvent, 4)
	done := make(chan struct{})
	go func() {
		ch <- ports.SaveEvent{State: ports.SavePending}
		select {
		case <-done:
			close(ch)
			return
		default:
		}
		ch <- ports.SaveEvent{State: ports.SaveValidating}
		s.mu.Lock()
		err := apply()
		s.mu.Unlock()
		if err != nil {
			ch <- ports.SaveEvent{State: ports.SaveFailed, Message: err.Error()}
		} else {
			ch <- ports.SaveEvent{State: ports.SaveSaved}
		}
		close(ch)
	}()
	return &saveHandle{id: id, events: ch, cancel: func() { close(done) }}
}

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
	}
}

func mcpServerViewToSettings(v ports.MCPServerView) config.MCPServerSettings {
	return config.MCPServerSettings{
		ID:        v.ID,
		Transport: v.Transport,
		Command:   v.Command,
		Args:      v.Args,
		Endpoint:  v.Endpoint,
		EnvNames:  v.EnvNames,
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

func (s *SettingsStore) applyGeneral(e ports.GeneralEdit) error {
	var mouseNotifier func(bool)
	switch v := e.(type) {
	case ports.SetTheme:
		s.general.Theme = v.Name
	case ports.SetMouse:
		s.general.Mouse = v.On
		mouseNotifier = s.mouseNotifier // fired below, after the persist, outside any lock
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
				ShowIterationNotices:   s.general.ShowIterationNotices,
				ShowPromptCacheNotices: s.general.ShowPromptCacheNotices,
			})
		}
	case ports.SetShowPromptCacheNotices:
		s.general.ShowPromptCacheNotices = v.On
		if s.res != nil {
			s.res.ShowPromptCacheNotices = v.On
		}
		if s.conv != nil {
			s.conv.SetNoticeOptions(TranslateOptions{
				ShowIterationNotices:   s.general.ShowIterationNotices,
				ShowPromptCacheNotices: s.general.ShowPromptCacheNotices,
			})
		}
	case ports.SetScrollLines:
		if v.N <= 0 {
			return fmt.Errorf("scroll lines must be positive")
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
	case ports.SetFullDiskAccess:
		// USER-config-only persistence, restart-to-apply - see
		// applySetFullDiskAccess for the provenance rules (audit F2/AR-4).
		return s.applySetFullDiskAccess(v.On)
	default:
		return fmt.Errorf("unknown general edit %T", e)
	}

	if cfgPath := s.configPath(); cfgPath != "" {
		if err := config.UpdateGeneralConfig(cfgPath, generalViewToSettings(s.general)); err != nil {
			return fmt.Errorf("persist general settings: %w", err)
		}
	}
	if mouseNotifier != nil {
		on := s.general.Mouse
		go mouseNotifier(on)
	}
	return nil
}

func (s *SettingsStore) initMCPFromConfig() {
	userPath := s.configPath()
	projectPath := s.projectConfigPath()

	userServers, projectServers, _ := config.LoadScopeMCPServers(userPath, projectPath)

	var failures map[string]error
	if s.agentState != nil && s.agentState.MCPManager != nil {
		failures = s.agentState.MCPManager.Failures()
	}

	addServer := func(srv config.MCPServerConfig, scope ports.Scope, global bool) {
		state := ports.MCPStateUnknown
		failMsg := ""
		failKind := ports.MCPFailNone
		if failures != nil {
			if err, failed := failures[srv.ID]; failed {
				state = ports.MCPStateFailed
				failMsg = err.Error()
			}
		}
		var headers map[string]string
		if len(srv.Headers) > 0 {
			headers = make(map[string]string, len(srv.Headers))
			for _, h := range srv.Headers {
				headers[h.Name] = h.ValueEnv
			}
		}

		s.mcp = append(s.mcp, ports.MCPServerView{
			ID:             srv.ID,
			Transport:      srv.Transport,
			Command:        srv.Command,
			Args:           srv.Args,
			Endpoint:       urlToEndpoint(srv.URL),
			EnvNames:       srv.Env,
			HeaderEnvNames: headers,
			Enabled:        true,
			Global:         global,
			TimeoutSeconds: srv.TimeoutSeconds,
			Scope:          scope,
			State:          state,
			FailKind:       failKind,
			FailMessage:    failMsg,
			OriginLabel:    mcpOriginLabel(scope, global),
		})
	}

	for _, srv := range userServers {
		addServer(srv, ports.ScopeUser, true)
	}
	for _, srv := range projectServers {
		addServer(srv, ports.ScopeProject, false)
	}

	if len(s.mcp) == 0 && s.res != nil && len(s.res.MCP.Servers) > 0 {
		for _, srv := range s.res.MCP.Servers {
			scope := ports.ScopeUser
			if !srv.Global {
				scope = ports.ScopeProject
			}
			addServer(srv, scope, srv.Global)
		}
	}
}

func urlToEndpoint(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if u.Host == "" {
		return raw
	}
	return fmt.Sprintf("%s://%s%s", u.Scheme, u.Host, u.Path)
}

// settingsMCP
type settingsMCP struct{ *SettingsStore }

func (m settingsMCP) MCPServers() []ports.MCPServerView {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ports.MCPServerView, len(m.mcp))
	copy(out, m.mcp)
	return out
}

func (m settingsMCP) Apply(_ context.Context, scope ports.Scope, e ports.MCPEdit) (ports.SaveHandle, error) {
	return m.newSaveHandle(func() error { return m.applyMCP(e, scope) }), nil
}

func (s *SettingsStore) findMCPServer(id string) int {
	for i := range s.mcp {
		if s.mcp[i].ID == id {
			return i
		}
	}
	return -1
}

func (s *SettingsStore) applyMCP(e ports.MCPEdit, scope ports.Scope) error {
	cfgPath := s.configPath()
	if scope == ports.ScopeProject {
		if p := s.projectConfigPath(); p != "" {
			cfgPath = p
		}
	}
	switch v := e.(type) {
	case ports.UpsertMCPServer:
		if i := s.findMCPServer(v.Server.ID); i >= 0 {
			s.mcp[i] = v.Server
		} else {
			s.mcp = append(s.mcp, v.Server)
		}
		if cfgPath != "" {
			if err := config.UpdateMCPServerConfig(cfgPath, mcpServerViewToSettings(v.Server)); err != nil {
				return fmt.Errorf("persist mcp server: %w", err)
			}
		}
	case ports.RemoveMCPServer:
		i := s.findMCPServer(v.ID)
		if i < 0 {
			return fmt.Errorf("mcp server %q not found", v.ID)
		}
		s.mcp = append(s.mcp[:i], s.mcp[i+1:]...)
		if cfgPath != "" {
			if err := config.RemoveMCPServerConfig(cfgPath, v.ID); err != nil {
				return fmt.Errorf("persist mcp server removal: %w", err)
			}
		}
	case ports.SetMCPServerEnabled:
		i := s.findMCPServer(v.ID)
		if i < 0 {
			return fmt.Errorf("mcp server %q not found", v.ID)
		}
		s.mcp[i].Enabled = v.On
		if cfgPath != "" {
			if err := config.UpdateMCPServerConfig(cfgPath, mcpServerViewToSettings(s.mcp[i])); err != nil {
				return fmt.Errorf("persist mcp server: %w", err)
			}
		}
	default:
		return fmt.Errorf("unknown mcp edit %T", e)
	}
	return nil
}

// settingsAgents is implemented in settings_agents.go

// settingsSkills is implemented in settings_skills.go
