package settings

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// mockSettings is an in-memory test double for ports.Settings.
type mockSettings struct {
	mu          sync.Mutex
	saveSeq     int
	general     ports.GeneralView
	providers   []ports.ProviderView
	mcp         []ports.MCPServerView
	agents      []ports.AgentView
	automations []ports.Automation
	runs        map[string][]ports.Run
	watchers    map[string][]chan ports.Run
	skills      []ports.SkillView
}

func newMockSettings() *mockSettings {
	return &mockSettings{
		general:     seedGeneral(),
		providers:   seedProviders(),
		mcp:         seedMCPServers(),
		agents:      seedAgents(),
		automations: seedAutomations(),
		runs:        make(map[string][]ports.Run),
		watchers:    make(map[string][]chan ports.Run),
		skills:      seedSkills(),
	}
}

func (m *mockSettings) SettingsAdapters() ports.Settings {
	return ports.Settings{
		General:     mockGeneral{m},
		Providers:   mockProviders{m},
		MCP:         mockMCP{m},
		Agents:      mockAgents{m},
		Automations: mockAutomations{m},
		Skills:      mockSkills{m},
	}
}

var (
	_ ports.GeneralSettings    = mockGeneral{}
	_ ports.ProviderSettings   = mockProviders{}
	_ ports.MCPSettings        = mockMCP{}
	_ ports.AgentSettings      = mockAgents{}
	_ ports.AutomationSettings = mockAutomations{}
	_ ports.SkillSettings      = mockSkills{}
)

type mockSaveHandle struct {
	id     string
	events chan ports.SaveEvent
	cancel func()
}

func (h *mockSaveHandle) ID() string                     { return h.id }
func (h *mockSaveHandle) Events() <-chan ports.SaveEvent { return h.events }
func (h *mockSaveHandle) Cancel()                        { h.cancel() }

func (m *mockSettings) newSaveHandle(apply func() error) ports.SaveHandle {
	m.mu.Lock()
	m.saveSeq++
	id := fmt.Sprintf("save-%d", m.saveSeq)
	m.mu.Unlock()

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
		m.mu.Lock()
		err := apply()
		m.mu.Unlock()
		if err != nil {
			ch <- ports.SaveEvent{State: ports.SaveFailed, Message: err.Error()}
		} else {
			ch <- ports.SaveEvent{State: ports.SaveSaved}
		}
		close(ch)
	}()
	return &mockSaveHandle{id: id, events: ch, cancel: func() { close(done) }}
}

var timeNow = time.Now

// --- General ---

type mockGeneral struct{ *mockSettings }

func (g mockGeneral) General() ports.GeneralView {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.general
}

func (g mockGeneral) Apply(_ context.Context, _ ports.Scope, e ports.GeneralEdit) (ports.SaveHandle, error) {
	return g.newSaveHandle(func() error { return g.applyGeneral(e) }), nil
}

func (m *mockSettings) applyGeneral(e ports.GeneralEdit) error {
	switch v := e.(type) {
	case ports.SetTheme:
		m.general.Theme = v.Name
	case ports.SetMouse:
		m.general.Mouse = v.On
	case ports.SetShowReasoning:
		m.general.ShowReasoning = v.On
	case ports.SetShowIterationNotices:
		m.general.ShowIterationNotices = v.On
	case ports.SetShowPromptCacheNotices:
		m.general.ShowPromptCacheNotices = v.On
	case ports.SetScrollLines:
		if v.N <= 0 {
			return fmt.Errorf("scroll lines must be positive")
		}
		m.general.ScrollLines = v.N
	case ports.SetApprovalDefault:
		m.general.ApprovalDefault = v.Mode
	case ports.SetScreenReader:
		m.general.ScreenReader = v.On
	case ports.SetReducedMotion:
		m.general.ReducedMotion = v.On
	default:
		return fmt.Errorf("unknown general edit %T", e)
	}
	return nil
}

// --- Providers ---

type mockProviders struct{ *mockSettings }

func (p mockProviders) Providers() []ports.ProviderView {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]ports.ProviderView, len(p.providers))
	copy(out, p.providers)
	return out
}

func (p mockProviders) Apply(_ context.Context, _ ports.Scope, e ports.ProviderEdit) (ports.SaveHandle, error) {
	return p.newSaveHandle(func() error { return p.applyProvider(e) }), nil
}

func (m *mockSettings) findProvider(name string) int {
	for i := range m.providers {
		if m.providers[i].Name == name {
			return i
		}
	}
	return -1
}

func (m *mockSettings) applyProvider(e ports.ProviderEdit) error {
	switch v := e.(type) {
	case ports.UpsertProvider:
		if i := m.findProvider(v.Provider.Name); i >= 0 {
			m.providers[i] = v.Provider
			return nil
		}
		m.providers = append(m.providers, v.Provider)
	case ports.RemoveProvider:
		i := m.findProvider(v.Name)
		if i < 0 {
			return fmt.Errorf("provider %q not found", v.Name)
		}
		m.providers = append(m.providers[:i], m.providers[i+1:]...)
	case ports.UpsertModel:
		i := m.findProvider(v.Provider)
		if i < 0 {
			return fmt.Errorf("provider %q not found", v.Provider)
		}
		return m.upsertModel(i, v.Model)
	case ports.RemoveModel:
		i := m.findProvider(v.Provider)
		if i < 0 {
			return fmt.Errorf("provider %q not found", v.Provider)
		}
		return m.removeModel(i, v.Model)
	case ports.ActivateModel:
		return m.activateModel(v.Provider, v.Model)
	case ports.SetDefaultModel:
		return m.setDefaultModel(v.Provider, v.Model)
	default:
		return fmt.Errorf("unknown provider edit %T", e)
	}
	return nil
}

func (m *mockSettings) upsertModel(providerIdx int, mv ports.ModelView) error {
	models := m.providers[providerIdx].Models
	for i := range models {
		if models[i].Name == mv.Name {
			models[i] = mv
			return nil
		}
	}
	m.providers[providerIdx].Models = append(models, mv)
	return nil
}

func (m *mockSettings) removeModel(providerIdx int, name string) error {
	models := m.providers[providerIdx].Models
	for i := range models {
		if models[i].Name == name {
			m.providers[providerIdx].Models = append(models[:i], models[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("model %q not found", name)
}

func (m *mockSettings) activateModel(provider, model string) error {
	i := m.findProvider(provider)
	if i < 0 {
		return fmt.Errorf("provider %q not found", provider)
	}
	found := false
	for _, mdl := range m.providers[i].Models {
		if mdl.Name == model {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("model %q not found under provider %q", model, provider)
	}
	for j := range m.providers {
		m.providers[j].Active = j == i
	}
	m.providers[i].ActiveModel = model
	return nil
}

func (m *mockSettings) setDefaultModel(provider, model string) error {
	i := m.findProvider(provider)
	if i < 0 {
		return fmt.Errorf("provider %q not found", provider)
	}
	found := false
	for _, mdl := range m.providers[i].Models {
		if mdl.Name == model {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("model %q not found under provider %q", model, provider)
	}
	m.providers[i].DefaultModel = model
	return nil
}

// --- MCP ---

type mockMCP struct{ *mockSettings }

func (m mockMCP) MCPServers() []ports.MCPServerView {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ports.MCPServerView, len(m.mcp))
	copy(out, m.mcp)
	return out
}

func (m mockMCP) Apply(_ context.Context, _ ports.Scope, e ports.MCPEdit) (ports.SaveHandle, error) {
	return m.newSaveHandle(func() error { return m.applyMCP(e) }), nil
}

func (m *mockSettings) findMCPServer(id string) int {
	for i := range m.mcp {
		if m.mcp[i].ID == id {
			return i
		}
	}
	return -1
}

func (m *mockSettings) applyMCP(e ports.MCPEdit) error {
	switch v := e.(type) {
	case ports.UpsertMCPServer:
		if i := m.findMCPServer(v.Server.ID); i >= 0 {
			m.mcp[i] = v.Server
			return nil
		}
		m.mcp = append(m.mcp, v.Server)
	case ports.RemoveMCPServer:
		i := m.findMCPServer(v.ID)
		if i < 0 {
			return fmt.Errorf("mcp server %q not found", v.ID)
		}
		m.mcp = append(m.mcp[:i], m.mcp[i+1:]...)
	case ports.SetMCPServerEnabled:
		i := m.findMCPServer(v.ID)
		if i < 0 {
			return fmt.Errorf("mcp server %q not found", v.ID)
		}
		m.mcp[i].Enabled = v.On
		m.mcp[i].State = ports.MCPStateUnknown
	default:
		return fmt.Errorf("unknown mcp edit %T", e)
	}
	return nil
}

// --- Agents ---

type mockAgents struct{ *mockSettings }

func (a mockAgents) Agents() []ports.AgentView {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]ports.AgentView, len(a.agents))
	copy(out, a.agents)
	return out
}

func (a mockAgents) Apply(_ context.Context, scope ports.Scope, e ports.AgentEdit) (ports.SaveHandle, error) {
	return a.newSaveHandle(func() error { return a.applyAgent(scope, e) }), nil
}

func (m *mockSettings) findAgent(name string) int {
	for i := range m.agents {
		if m.agents[i].Name == name {
			return i
		}
	}
	return -1
}

func (m *mockSettings) applyAgent(scope ports.Scope, e ports.AgentEdit) error {
	switch v := e.(type) {
	case ports.UpsertAgent:
		v.Agent.Scope = scope
		if i := m.findAgent(v.Agent.Name); i >= 0 {
			m.agents[i] = v.Agent
			return nil
		}
		m.agents = append(m.agents, v.Agent)
	case ports.RemoveAgent:
		if v.Name == ports.DefaultAgentName {
			return fmt.Errorf("the default agent %q cannot be removed", ports.DefaultAgentName)
		}
		i := m.findAgent(v.Name)
		if i < 0 {
			return fmt.Errorf("agent %q not found", v.Name)
		}
		m.agents = append(m.agents[:i], m.agents[i+1:]...)
	default:
		return fmt.Errorf("unknown agent edit %T", e)
	}
	return nil
}

// --- Automations ---

const runSimStep = 5 * time.Millisecond

type mockAutomations struct{ *mockSettings }

func (a mockAutomations) Automations() []ports.Automation {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]ports.Automation, len(a.automations))
	copy(out, a.automations)
	return out
}

func (a mockAutomations) Runs(automationID string, limit int) []ports.Run {
	a.mu.Lock()
	defer a.mu.Unlock()
	runs := a.runs[automationID]
	out := make([]ports.Run, len(runs))
	copy(out, runs)
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (a mockAutomations) Run(runID string) (ports.Run, bool) {
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

func (a mockAutomations) Apply(_ context.Context, _ ports.Scope, e ports.AutomationEdit) (ports.SaveHandle, error) {
	if trig, ok := e.(ports.TriggerAutomation); ok {
		return a.newSaveHandle(func() error { return a.startRun(trig.ID) }), nil
	}
	return a.newSaveHandle(func() error { return a.applyAutomation(e) }), nil
}

func (m *mockSettings) findAutomation(id string) int {
	for i := range m.automations {
		if m.automations[i].ID == id {
			return i
		}
	}
	return -1
}

func (m *mockSettings) applyAutomation(e ports.AutomationEdit) error {
	switch v := e.(type) {
	case ports.UpsertAutomation:
		if i := m.findAutomation(v.Automation.ID); i >= 0 {
			m.automations[i] = v.Automation
			return nil
		}
		m.automations = append(m.automations, v.Automation)
	case ports.RemoveAutomation:
		i := m.findAutomation(v.ID)
		if i < 0 {
			return fmt.Errorf("automation %q not found", v.ID)
		}
		m.automations = append(m.automations[:i], m.automations[i+1:]...)
	case ports.SetAutomationEnabled:
		i := m.findAutomation(v.ID)
		if i < 0 {
			return fmt.Errorf("automation %q not found", v.ID)
		}
		m.automations[i].Enabled = v.On
	default:
		return fmt.Errorf("unknown automation edit %T", e)
	}
	return nil
}

func (m *mockSettings) startRun(automationID string) error {
	i := m.findAutomation(automationID)
	if i < 0 {
		return fmt.Errorf("automation %q not found", automationID)
	}
	m.saveSeq++
	run := ports.Run{
		ID: fmt.Sprintf("run-%d", m.saveSeq), AutomationID: automationID,
		Trigger: ports.TriggerManual, State: ports.RunPending, StartedAt: timeNow(),
	}
	m.runs[automationID] = append(m.runs[automationID], run)
	summary := ports.RunSummary{ID: run.ID, State: run.State, StartedAt: run.StartedAt}
	m.automations[i].LastRun = &summary
	m.publishRunLocked(run)

	go m.advanceRun(automationID, run.ID)
	return nil
}

func (m *mockSettings) advanceRun(automationID, runID string) {
	time.Sleep(runSimStep)
	m.updateRun(automationID, runID, ports.RunRunning, false)
	time.Sleep(runSimStep)
	m.updateRun(automationID, runID, ports.RunSucceeded, true)
}

func (m *mockSettings) updateRun(automationID, runID string, state ports.RunState, ended bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	runs := m.runs[automationID]
	for j := range runs {
		if runs[j].ID != runID {
			continue
		}
		runs[j].State = state
		if ended {
			now := timeNow()
			runs[j].EndedAt = &now
		}
		if i := m.findAutomation(automationID); i >= 0 {
			m.automations[i].LastRun = &ports.RunSummary{
				ID: runs[j].ID, State: runs[j].State, StartedAt: runs[j].StartedAt,
			}
		}
		m.publishRunLocked(runs[j])
		return
	}
}

type runWatch struct {
	ch     chan ports.Run
	cancel func()
}

func (w *runWatch) Events() <-chan ports.Run { return w.ch }
func (w *runWatch) Cancel()                  { w.cancel() }

func (a mockAutomations) Watch(_ context.Context, automationID string) (ports.RunHandle, error) {
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

func (m *mockSettings) publishRunLocked(run ports.Run) {
	for _, ch := range m.watchers[run.AutomationID] {
		select {
		case ch <- run:
		default:
		}
	}
}

func (m *mockSettings) removeWatcherLocked(automationID string, ch chan ports.Run) {
	watchers := m.watchers[automationID]
	for i, w := range watchers {
		if w == ch {
			m.watchers[automationID] = append(watchers[:i], watchers[i+1:]...)
			close(ch)
			return
		}
	}
}

// --- Skills ---

type mockSkills struct{ *mockSettings }

func (s mockSkills) Skills() []ports.SkillView {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ports.SkillView, len(s.skills))
	copy(out, s.skills)
	return out
}

func (s mockSkills) Apply(_ context.Context, _ ports.Scope, e ports.SkillEdit) (ports.SaveHandle, error) {
	return s.newSaveHandle(func() error { return s.applySkill(e) }), nil
}

func (m *mockSettings) findSkill(name string) int {
	for i := range m.skills {
		if m.skills[i].Name == name {
			return i
		}
	}
	return -1
}

func (m *mockSettings) applySkill(e ports.SkillEdit) error {
	switch v := e.(type) {
	case ports.RemoveSkill:
		i := m.findSkill(v.Name)
		if i < 0 {
			return fmt.Errorf("skill %q not found", v.Name)
		}
		m.skills = append(m.skills[:i], m.skills[i+1:]...)
	case ports.SetSkillUserInvocable:
		i := m.findSkill(v.Name)
		if i < 0 {
			return fmt.Errorf("skill %q not found", v.Name)
		}
		m.skills[i].UserInvocable = v.On
	case ports.SaveSkill:
		i := m.findSkill(v.Name)
		skill := ports.SkillView{
			Name:              v.Name,
			Description:       v.Description,
			Origin:            v.Origin,
			UserInvocable:     v.UserInvocable,
			Tools:             v.Tools,
			Triggers:          v.Triggers,
			Instructions:      v.Instructions,
			InstructionsChars: len(v.Instructions),
		}
		if i >= 0 {
			m.skills[i] = skill
		} else {
			m.skills = append(m.skills, skill)
		}
	default:
		return fmt.Errorf("unknown skill edit %T", e)
	}
	return nil
}

// --- Seed Data ---

func seedGeneral() ports.GeneralView {
	return ports.GeneralView{
		Theme:                  "mivia-dark",
		Mouse:                  true,
		ShowReasoning:          true,
		ShowIterationNotices:   false,
		ShowPromptCacheNotices: false,
		ScrollLines:            3,
		ApprovalDefault:        "once",
		ScreenReader:           false,
		ReducedMotion:          false,
	}
}

func seedProviders() []ports.ProviderView {
	return []ports.ProviderView{
		{
			Name: "openrouter", BaseURL: "https://openrouter.ai/api/v1",
			APIKeyEnv: "OPENROUTER_API_KEY", APIKeySet: true,
			Active: true, Selectable: true, ActiveModel: "anthropic/claude-opus-5", DefaultModel: "anthropic/claude-opus-5",
			Models: []ports.ModelView{
				{Name: "anthropic/claude-opus-5", ContextWindowTokens: 200_000, ReasoningEfforts: []string{"low", "high"}, Reasoning: "high"},
				{Name: "openai/gpt-5", ContextWindowTokens: 128_000},
			},
		},
		{
			Name: "ollama", BaseURL: "http://localhost:11434",
			APIKeyEnv: "", APIKeySet: false, Selectable: true, DefaultModel: "llama3.1",
			Models: []ports.ModelView{
				{Name: "llama3.1", ContextWindowTokens: 128_000},
			},
		},
		{
			Name: "deepseek", BaseURL: "https://api.deepseek.com",
			APIKeyEnv: "DEEPSEEK_API_KEY", APIKeySet: false,
			Selectable: false, DisabledReason: "credential unavailable",
			BuiltIn: true,
		},
	}
}

func seedMCPServers() []ports.MCPServerView {
	return []ports.MCPServerView{
		{
			ID: "filesystem", Transport: "stdio", Command: "npx",
			Args:    []string{"-y", "@modelcontextprotocol/server-filesystem", "--token=sk-test-not-real-canary"},
			Enabled: true, TimeoutSeconds: 30, State: ports.MCPStateConnected, ToolCount: 6,
		},
		{
			ID: "search", Transport: "streamable_http",
			Endpoint: "https://search.example.internal/mcp",
			EnvNames: []string{"SEARCH_API_KEY"}, Enabled: true, TimeoutSeconds: 15,
			State: ports.MCPStateFailed, FailKind: ports.MCPFailAuth,
			FailMessage: "authentication failed", ToolCount: 0,
		},
	}
}

func seedAgents() []ports.AgentView {
	return []ports.AgentView{
		{Name: ports.DefaultAgentName, Description: "general purpose orchestrator", Provider: "openrouter", Model: "anthropic/claude-opus-5", MaxTurns: 40, SystemPromptChars: 4200, Scope: ports.ScopeUser},
		{Name: "go-engineer", Description: "implements Go changes", Provider: "openrouter", Model: "anthropic/claude-opus-5", Tools: []string{"edit_file", "run_command"}, MaxTurns: 60, SystemPromptChars: 2100, Scope: ports.ScopeUser},
		{Name: "reviewer", Description: "reviews changed code for this workspace", Provider: "openrouter", Model: "anthropic/claude-opus-5", Tools: []string{"read_file", "inspect_repository"}, Skills: []string{"code-review"}, MaxTurns: 30, SystemPromptChars: 1800, Scope: ports.ScopeProject},
	}
}

func seedAutomations() []ports.Automation {
	return []ports.Automation{
		{
			ID: "nightly-audit", Name: "Nightly bug audit", Description: "runs the fast bug audit workflow",
			Enabled: true,
			Trigger: ports.TriggerSpec{Kind: ports.TriggerScheduled, Schedule: &ports.ScheduleSpec{
				Kind: ports.ScheduleRecurring, Cron: "0 2 * * *", TZ: "UTC",
			}},
			Action: ports.ActionRef{Workflow: "bug-fix-fast"},
		},
		{
			ID: "manual-release-check", Name: "Release checklist", Description: "manual pre-release verification",
			Enabled: true,
			Trigger: ports.TriggerSpec{Kind: ports.TriggerManual},
			Action:  ports.ActionRef{Workflow: "feature-delivery"},
		},
	}
}

func seedSkills() []ports.SkillView {
	return []ports.SkillView{
		{
			Name:              "code-review",
			Description:       "review changed code for quality, correctness, and security",
			Origin:            "project",
			Tools:             []string{"inspect_repository", "read_file"},
			UserInvocable:     true,
			InstructionsChars: 850,
			Instructions:      "# Code Review\nReview changed code for quality, correctness, and security.\nCheck for edge cases and adherence to ADLC.",
		},
		{
			Name:              "test-runner",
			Description:       "run workspace test suites and report failures",
			Origin:            "user",
			Tools:             []string{"run_command"},
			UserInvocable:     true,
			InstructionsChars: 420,
			Instructions:      "# Test Runner\nRun fast tests first using `make verify-fast`, then full verification.",
		},
	}
}
