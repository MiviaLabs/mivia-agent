package uiadapter

import (
	"context"
	"fmt"
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

	mu sync.Mutex

	general     ports.GeneralView
	providers   []ports.ProviderView
	mcp         []ports.MCPServerView
	agents      []ports.AgentView
	automations []ports.Automation
	runs        map[string][]ports.Run
	watchers    map[string][]chan ports.Run

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

func (s *SettingsStore) initFromConfig() {
	showIter := false
	showCache := false
	if s.res != nil {
		showIter = s.res.ShowIterationNotices
		showCache = s.res.ShowPromptCacheNotices
	}
	s.general = ports.GeneralView{
		Theme:                  "mivia-dark",
		Mouse:                  true,
		ShowReasoning:          true,
		ShowIterationNotices:   showIter,
		ShowPromptCacheNotices: showCache,
		ScrollLines:            3,
		ApprovalDefault:        "once",
		ScreenReader:           false,
		ReducedMotion:          false,
	}
	s.initProvidersFromConfig()
	s.initAgentsFromConfig()
}

func (s *SettingsStore) initProvidersFromConfig() {
	if s.res == nil {
		return
	}
	catalog := s.res.ModelCatalog()
	if len(catalog) == 0 && s.res.ProviderName != "" {
		var models []ports.ModelView
		if len(s.res.ModelProfiles) > 0 {
			for _, p := range s.res.ModelProfiles {
				models = append(models, ports.ModelView{Name: p.Name, ContextWindowTokens: p.ContextWindowTokens})
			}
		} else if len(s.res.Models) > 0 {
			for _, m := range s.res.Models {
				models = append(models, ports.ModelView{Name: m, ContextWindowTokens: 128000})
			}
		} else if s.res.Model != "" {
			models = append(models, ports.ModelView{Name: s.res.Model, ContextWindowTokens: 128000})
		}
		activeModel := s.res.Model
		if s.sess != nil && s.sess.CurrentSelection().Model != "" {
			activeModel = s.sess.CurrentSelection().Model
		}
		s.providers = append(s.providers, ports.ProviderView{
			Name:         s.res.ProviderName,
			Active:       true,
			Selectable:   true,
			Models:       models,
			ActiveModel:  activeModel,
			DefaultModel: s.res.Model,
		})
	}
	for _, g := range catalog {
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
		defaultModel := g.DefaultModel
		if defaultModel == "" && len(g.Models) > 0 {
			defaultModel = g.Models[0].Name
		}
		s.providers = append(s.providers, ports.ProviderView{
			Name:           g.Provider,
			Active:         active,
			Selectable:     g.Selectable,
			DisabledReason: g.DisabledReason,
			Models:         models,
			ActiveModel:    activeModel,
			DefaultModel:   defaultModel,
		})
	}
}

func (s *SettingsStore) initAgentsFromConfig() {
	if s.agentState == nil || s.agentState.Registry == nil {
		return
	}
	for _, a := range s.agentState.Registry.List() {
		var skills []string
		if a.Skills != nil {
			skills = *a.Skills
		}
		s.agents = append(s.agents, ports.AgentView{
			Name:              a.Name,
			Description:       a.Description,
			Provider:          a.Provider,
			Model:             a.Model,
			Tools:             a.EffectiveTools,
			Skills:            skills,
			MCPServers:        a.EffectiveMCPServers,
			SystemPromptChars: len(a.SystemPrompt),
		})
	}
}

// Settings returns the ports.Settings bundle with all section adapters.
func (s *SettingsStore) Settings() ports.Settings {
	return ports.Settings{
		General:     settingsGeneral{s},
		Providers:   settingsProviders{s},
		MCP:         settingsMCP{s},
		Agents:      settingsAgents{s},
		Automations: settingsAutomations{s},
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

func providerViewToSettings(v ports.ProviderView) config.ProviderSettings {
	var models []config.ModelSettings
	for _, m := range v.Models {
		models = append(models, config.ModelSettings{
			Name:                m.Name,
			ContextWindowTokens: m.ContextWindowTokens,
			MaxOutputTokens:     m.MaxOutputTokens,
			Reasoning:           m.Reasoning,
			ReasoningEfforts:    m.ReasoningEfforts,
		})
	}
	return config.ProviderSettings{
		Name:         v.Name,
		BaseURL:      v.BaseURL,
		APIKeyEnv:    v.APIKeyEnv,
		DefaultModel: v.DefaultModel,
		Models:       models,
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

func agentViewToSettings(v ports.AgentView) config.AgentFileSettings {
	return config.AgentFileSettings{
		Name:        v.Name,
		Description: v.Description,
		Provider:    v.Provider,
		Model:       v.Model,
		Tools:       v.Tools,
		Skills:      v.Skills,
		MCPServers:  v.MCPServers,
	}
}

func (s *SettingsStore) applyGeneral(e ports.GeneralEdit) error {
	switch v := e.(type) {
	case ports.SetTheme:
		s.general.Theme = v.Name
	case ports.SetMouse:
		s.general.Mouse = v.On
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
		s.general.ApprovalDefault = v.Mode
		if s.res != nil {
			s.res.Approvals.DefaultMode = v.Mode
		}
	case ports.SetScreenReader:
		s.general.ScreenReader = v.On
	case ports.SetReducedMotion:
		s.general.ReducedMotion = v.On
	default:
		return fmt.Errorf("unknown general edit %T", e)
	}

	if cfgPath := s.configPath(); cfgPath != "" {
		_ = config.UpdateGeneralConfig(cfgPath, generalViewToSettings(s.general))
	}
	return nil
}

// settingsProviders
type settingsProviders struct{ *SettingsStore }

func (p settingsProviders) Providers() []ports.ProviderView {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]ports.ProviderView, len(p.providers))
	copy(out, p.providers)
	return out
}

func (p settingsProviders) Apply(_ context.Context, _ ports.Scope, e ports.ProviderEdit) (ports.SaveHandle, error) {
	return p.newSaveHandle(func() error { return p.applyProvider(e) }), nil
}

func (s *SettingsStore) findProvider(name string) int {
	for i := range s.providers {
		if s.providers[i].Name == name {
			return i
		}
	}
	return -1
}

func (s *SettingsStore) configPath() string {
	if s.res != nil && s.res.ConfigPath != "" {
		return s.res.ConfigPath
	}
	return config.UserConfigPath()
}

func (s *SettingsStore) applyProvider(e ports.ProviderEdit) error {
	cfgPath := s.configPath()
	switch v := e.(type) {
	case ports.UpsertProvider:
		return s.applyUpsertProvider(v, cfgPath)
	case ports.RemoveProvider:
		return s.applyRemoveProvider(v, cfgPath)
	case ports.UpsertModel:
		return s.applyUpsertModel(v, cfgPath)
	case ports.RemoveModel:
		return s.applyRemoveModel(v, cfgPath)
	case ports.ActivateModel:
		return s.applyActivateModel(v)
	case ports.SetDefaultModel:
		return s.applySetDefaultModel(v)
	default:
		return fmt.Errorf("unknown provider edit %T", e)
	}
}

func (s *SettingsStore) applyUpsertProvider(v ports.UpsertProvider, cfgPath string) error {
	if i := s.findProvider(v.Provider.Name); i >= 0 {
		s.providers[i] = v.Provider
	} else {
		s.providers = append(s.providers, v.Provider)
	}
	if cfgPath != "" {
		_ = config.UpdateProviderConfig(cfgPath, providerViewToSettings(v.Provider))
	}
	return nil
}

func (s *SettingsStore) applyRemoveProvider(v ports.RemoveProvider, cfgPath string) error {
	i := s.findProvider(v.Name)
	if i < 0 {
		return fmt.Errorf("provider %q not found", v.Name)
	}
	s.providers = append(s.providers[:i], s.providers[i+1:]...)
	if cfgPath != "" {
		_ = config.RemoveProviderConfig(cfgPath, v.Name)
	}
	return nil
}

func (s *SettingsStore) applyUpsertModel(v ports.UpsertModel, cfgPath string) error {
	i := s.findProvider(v.Provider)
	if i < 0 {
		return fmt.Errorf("provider %q not found", v.Provider)
	}
	found := false
	for j := range s.providers[i].Models {
		if s.providers[i].Models[j].Name == v.Model.Name {
			s.providers[i].Models[j] = v.Model
			found = true
			break
		}
	}
	if !found {
		s.providers[i].Models = append(s.providers[i].Models, v.Model)
	}
	if cfgPath != "" {
		_ = config.UpdateProviderConfig(cfgPath, providerViewToSettings(s.providers[i]))
	}
	return nil
}

func (s *SettingsStore) applyRemoveModel(v ports.RemoveModel, cfgPath string) error {
	i := s.findProvider(v.Provider)
	if i < 0 {
		return fmt.Errorf("provider %q not found", v.Provider)
	}
	models := s.providers[i].Models
	removed := false
	for j := range models {
		if models[j].Name == v.Model {
			s.providers[i].Models = append(models[:j], models[j+1:]...)
			removed = true
			break
		}
	}
	if !removed {
		return fmt.Errorf("model %q not found under %q", v.Model, v.Provider)
	}
	if cfgPath != "" {
		_ = config.UpdateProviderConfig(cfgPath, providerViewToSettings(s.providers[i]))
	}
	return nil
}

func (s *SettingsStore) applyActivateModel(v ports.ActivateModel) error {
	target := s.findProvider(v.Provider)
	if target < 0 {
		return fmt.Errorf("provider %q not found", v.Provider)
	}
	foundModel := false
	for _, m := range s.providers[target].Models {
		if m.Name == v.Model {
			foundModel = true
			break
		}
	}
	if !foundModel {
		return fmt.Errorf("model %q not found under %q", v.Model, v.Provider)
	}
	if s.sess != nil && s.res != nil {
		if _, err := cliagents.SwitchModelCommand(s.sess, s.res, v.Provider, v.Model); err != nil {
			return fmt.Errorf("failed to switch model to %q (%s): %w", v.Model, v.Provider, err)
		}
		s.res.ProviderName = v.Provider
		s.res.Model = v.Model
	}
	for i := range s.providers {
		s.providers[i].Active = (i == target)
	}
	s.providers[target].ActiveModel = v.Model
	if cfgPath := s.configPath(); cfgPath != "" {
		_ = config.UpdateActiveModelConfig(cfgPath, v.Provider, v.Model)
	}
	return nil
}

func (s *SettingsStore) applySetDefaultModel(v ports.SetDefaultModel) error {
	target := s.findProvider(v.Provider)
	if target < 0 {
		return fmt.Errorf("provider %q not found", v.Provider)
	}
	foundModel := false
	for _, m := range s.providers[target].Models {
		if m.Name == v.Model {
			foundModel = true
			break
		}
	}
	if !foundModel {
		return fmt.Errorf("model %q not found under %q", v.Model, v.Provider)
	}
	cfgPath := s.configPath()
	if cfgPath != "" {
		if err := config.UpdateProviderDefaultModel(cfgPath, v.Provider, v.Model); err != nil {
			return fmt.Errorf("failed to persist default model: %w", err)
		}
	}
	s.providers[target].DefaultModel = v.Model
	return nil
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

func (m settingsMCP) Apply(_ context.Context, _ ports.Scope, e ports.MCPEdit) (ports.SaveHandle, error) {
	return m.newSaveHandle(func() error { return m.applyMCP(e) }), nil
}

func (s *SettingsStore) findMCPServer(id string) int {
	for i := range s.mcp {
		if s.mcp[i].ID == id {
			return i
		}
	}
	return -1
}

func (s *SettingsStore) applyMCP(e ports.MCPEdit) error {
	cfgPath := s.configPath()
	switch v := e.(type) {
	case ports.UpsertMCPServer:
		if i := s.findMCPServer(v.Server.ID); i >= 0 {
			s.mcp[i] = v.Server
		} else {
			s.mcp = append(s.mcp, v.Server)
		}
		if cfgPath != "" {
			_ = config.UpdateMCPServerConfig(cfgPath, mcpServerViewToSettings(v.Server))
		}
	case ports.RemoveMCPServer:
		i := s.findMCPServer(v.ID)
		if i < 0 {
			return fmt.Errorf("mcp server %q not found", v.ID)
		}
		s.mcp = append(s.mcp[:i], s.mcp[i+1:]...)
		if cfgPath != "" {
			_ = config.RemoveMCPServerConfig(cfgPath, v.ID)
		}
	case ports.SetMCPServerEnabled:
		i := s.findMCPServer(v.ID)
		if i < 0 {
			return fmt.Errorf("mcp server %q not found", v.ID)
		}
		s.mcp[i].Enabled = v.On
		if cfgPath != "" {
			_ = config.UpdateMCPServerConfig(cfgPath, mcpServerViewToSettings(s.mcp[i]))
		}
	default:
		return fmt.Errorf("unknown mcp edit %T", e)
	}
	return nil
}

// settingsAgents
type settingsAgents struct{ *SettingsStore }

func (a settingsAgents) Agents() []ports.AgentView {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]ports.AgentView, len(a.agents))
	copy(out, a.agents)
	return out
}

func (a settingsAgents) Apply(_ context.Context, _ ports.Scope, e ports.AgentEdit) (ports.SaveHandle, error) {
	return a.newSaveHandle(func() error { return a.applyAgent(e) }), nil
}

func (s *SettingsStore) findAgent(name string) int {
	for i := range s.agents {
		if s.agents[i].Name == name {
			return i
		}
	}
	return -1
}

func (s *SettingsStore) applyAgent(e ports.AgentEdit) error {
	agentsDir := config.WorkspaceAgentsDir("")
	switch v := e.(type) {
	case ports.UpsertAgent:
		if i := s.findAgent(v.Agent.Name); i >= 0 {
			s.agents[i] = v.Agent
		} else {
			s.agents = append(s.agents, v.Agent)
		}
		if agentsDir != "" {
			_ = config.WriteAgentFile(agentsDir, agentViewToSettings(v.Agent), "")
		}
	case ports.RemoveAgent:
		if v.Name == ports.DefaultAgentName {
			return fmt.Errorf("the default agent %q cannot be removed", ports.DefaultAgentName)
		}
		i := s.findAgent(v.Name)
		if i < 0 {
			return fmt.Errorf("agent %q not found", v.Name)
		}
		s.agents = append(s.agents[:i], s.agents[i+1:]...)
		if agentsDir != "" {
			_ = config.RemoveAgentFile(agentsDir, v.Name)
		}
	default:
		return fmt.Errorf("unknown agent edit %T", e)
	}
	return nil
}

// settingsAutomations
type settingsAutomations struct{ *SettingsStore }

func (a settingsAutomations) Automations() []ports.Automation {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]ports.Automation, len(a.automations))
	copy(out, a.automations)
	return out
}

func (a settingsAutomations) Runs(automationID string, limit int) []ports.Run {
	a.mu.Lock()
	defer a.mu.Unlock()
	runs := a.runs[automationID]
	out := make([]ports.Run, len(runs))
	copy(out, runs)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (a settingsAutomations) Run(runID string) (ports.Run, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, runs := range a.runs {
		for _, r := range runs {
			if r.ID == runID {
				return r, true
			}
		}
	}
	return ports.Run{}, false
}

func (a settingsAutomations) Apply(_ context.Context, _ ports.Scope, e ports.AutomationEdit) (ports.SaveHandle, error) {
	if trig, ok := e.(ports.TriggerAutomation); ok {
		return a.newSaveHandle(func() error { return a.startRun(trig.ID) }), nil
	}
	return a.newSaveHandle(func() error { return a.applyAutomation(e) }), nil
}

func (s *SettingsStore) findAutomation(id string) int {
	for i := range s.automations {
		if s.automations[i].ID == id {
			return i
		}
	}
	return -1
}

func (s *SettingsStore) applyAutomation(e ports.AutomationEdit) error {
	switch v := e.(type) {
	case ports.UpsertAutomation:
		if i := s.findAutomation(v.Automation.ID); i >= 0 {
			s.automations[i] = v.Automation
			return nil
		}
		s.automations = append(s.automations, v.Automation)
	case ports.RemoveAutomation:
		i := s.findAutomation(v.ID)
		if i < 0 {
			return fmt.Errorf("automation %q not found", v.ID)
		}
		s.automations = append(s.automations[:i], s.automations[i+1:]...)
	case ports.SetAutomationEnabled:
		i := s.findAutomation(v.ID)
		if i < 0 {
			return fmt.Errorf("automation %q not found", v.ID)
		}
		s.automations[i].Enabled = v.On
	default:
		return fmt.Errorf("unknown automation edit %T", e)
	}
	return nil
}

func (s *SettingsStore) startRun(automationID string) error {
	i := s.findAutomation(automationID)
	if i < 0 {
		return fmt.Errorf("automation %q not found", automationID)
	}
	s.saveSeq++
	run := ports.Run{
		ID:           fmt.Sprintf("run-%d", s.saveSeq),
		AutomationID: automationID,
		Trigger:      ports.TriggerManual,
		State:        ports.RunPending,
	}
	s.runs[automationID] = append(s.runs[automationID], run)
	summary := ports.RunSummary{ID: run.ID, State: run.State}
	s.automations[i].LastRun = &summary
	return nil
}

type runWatch struct {
	ch     chan ports.Run
	cancel func()
}

func (w *runWatch) Events() <-chan ports.Run { return w.ch }
func (w *runWatch) Cancel()                  { w.cancel() }

func (a settingsAutomations) Watch(_ context.Context, automationID string) (ports.RunHandle, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.findAutomation(automationID) < 0 {
		return nil, fmt.Errorf("automation %q not found", automationID)
	}
	ch := make(chan ports.Run, 8)
	a.watchers[automationID] = append(a.watchers[automationID], ch)
	return &runWatch{
		ch: ch,
		cancel: func() {
			a.mu.Lock()
			defer a.mu.Unlock()
			a.removeWatcherLocked(automationID, ch)
		},
	}, nil
}

func (s *SettingsStore) removeWatcherLocked(automationID string, ch chan ports.Run) {
	watchers := s.watchers[automationID]
	for i, w := range watchers {
		if w == ch {
			s.watchers[automationID] = append(watchers[:i], watchers[i+1:]...)
			close(ch)
			return
		}
	}
}
