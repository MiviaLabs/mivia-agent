package settings

import (
	"context"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/component/field"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/keymap"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

type projectRow struct {
	id          int
	label       string
	f           field.Model
	isReadOnly  bool
	isChoice    bool
	configKey   string
	description string
	apply       func(value string) ports.ProjectEdit
}

type projectsSection struct {
	store         ports.ProjectSettings
	theme         theme.Theme
	tier          theme.Tier
	width, height int

	rows   []projectRow
	cursor int
	notice string

	editing      bool
	editRowIndex int
	editOrigVal  string
}

func newProjectsSection(store ports.ProjectSettings) *projectsSection {
	return &projectsSection{store: store}
}

func (s *projectsSection) Title() string { return "Projects" }

func (s *projectsSection) CapturingInput() bool {
	return s.editing
}

func (s *projectsSection) SetSize(w, h int) {
	s.width, s.height = w, h
	for i := range s.rows {
		s.rows[i].f.SetWidth(w - 16)
	}
}

func (s *projectsSection) SetTheme(t theme.Theme, tier theme.Tier) {
	s.theme, s.tier = t, tier
	for i := range s.rows {
		s.rows[i].f.SetTheme(t, tier)
	}
	if s.store != nil && len(s.rows) == 0 {
		s.rebuild()
	}
}

type projectFieldMaker struct {
	theme theme.Theme
	tier  theme.Tier
	width int
}

func (m projectFieldMaker) choice(label string, choices []string, active string) field.Model {
	f := field.New(m.theme, m.tier, label, field.KindChoice, m.width-16)
	f.SetChoices(choices, active)
	return f
}

func (m projectFieldMaker) text(label string, val string) field.Model {
	f := field.New(m.theme, m.tier, label, field.KindText, m.width-16)
	f.SetValue(val)
	return f
}

func (s *projectsSection) rebuild() {
	if s.store == nil {
		s.rows = nil
		return
	}
	p := s.store.Project()
	m := projectFieldMaker{theme: s.theme, tier: s.tier, width: s.width}

	s.rows = nil
	s.rows = append(s.rows, buildEnvironmentRows(p, m)...)
	s.rows = append(s.rows, buildChatOptionRows(p, m)...)
	s.rows = append(s.rows, buildToolStorageRows(p, m)...)

	if s.cursor >= len(s.rows) {
		s.cursor = len(s.rows) - 1
	}
	if s.cursor < 0 {
		s.cursor = 0
	}
}

func buildEnvironmentRows(p ports.ProjectView, m projectFieldMaker) []projectRow {
	wsDisplay := p.WorkspacePath
	if wsDisplay == "" {
		wsDisplay = "(current workspace)"
	}
	cfgDisplay := p.ConfigPath
	if cfgDisplay == "" {
		cfgDisplay = "(default overlay)"
	}

	return []projectRow{
		{
			id: 0, label: "Workspace Root:     ", isReadOnly: true,
			f:           m.text("Workspace Root:     ", wsDisplay),
			configKey:   "workspace.root",
			description: "Filesystem root directory of the active workspace project.",
		},
		{
			id: 1, label: "Config File:        ", isReadOnly: true,
			f:           m.text("Config File:        ", cfgDisplay),
			configKey:   "workspace.config_path",
			description: "Project configuration file path (.mivia/mivia.toml).",
		},
		{
			id: 2, label: "Env File:           ", isChoice: false,
			f:           m.text("Env File:           ", p.EnvFile),
			configKey:   "env_file",
			description: "Path to the environment variables / credentials file (default: ./.env).",
			apply: func(val string) ports.ProjectEdit {
				return ports.SetProjectEnvFile{Path: strings.TrimSpace(val)}
			},
		},
		{
			id: 3, label: "Branch Prefix:      ", isChoice: false,
			f:           m.text("Branch Prefix:      ", p.BranchPrefix),
			configKey:   "worktrees.branch_prefix",
			description: "Git branch prefix for linked worktrees created by mivia (e.g. mivia/).",
			apply: func(val string) ports.ProjectEdit {
				return ports.SetProjectBranchPrefix{Prefix: strings.TrimSpace(val)}
			},
		},
		{
			id: 4, label: "System Prompt:      ", isChoice: false,
			f:           m.text("System Prompt:      ", p.SystemPrompt),
			configKey:   "chat.system_prompt",
			description: "Default system prompt given to the root orchestrator agent in chat turns.",
			apply: func(val string) ports.ProjectEdit {
				return ports.SetProjectSystemPrompt{Prompt: strings.TrimSpace(val)}
			},
		},
	}
}

func buildChatOptionRows(p ports.ProjectView, m projectFieldMaker) []projectRow {
	return []projectRow{
		{
			id: 5, label: "Temperature:        ", isChoice: true,
			f:           m.choice("Temperature:        ", []string{"default", "0.0", "0.2", "0.5", "0.7", "1.0"}, p.Temperature),
			configKey:   "chat.temperature",
			description: "Sampling temperature for model generation (0.0 = deterministic, higher = creative).",
			apply: func(val string) ports.ProjectEdit {
				return ports.SetProjectTemperature{Value: val}
			},
		},
		{
			id: 6, label: "Max Tokens:         ", isChoice: true,
			f:           m.choice("Max Tokens:         ", []string{"default", "4096", "8192", "16384", "32768", "65536", "128000"}, p.MaxTokens),
			configKey:   "chat.max_tokens",
			description: "Maximum completion output tokens per turn (bounds response length).",
			apply: func(val string) ports.ProjectEdit {
				return ports.SetProjectMaxTokens{Value: val}
			},
		},
		{
			id: 7, label: "Max Prompt Tokens:  ", isChoice: true,
			f:           m.choice("Max Prompt Tokens:  ", []string{"default", "50000", "100000", "200000", "400000"}, p.MaxPromptTokens),
			configKey:   "chat.max_prompt_tokens",
			description: "Recommended prompt budget bound. Triggers compaction earlier on long sessions.",
			apply: func(val string) ports.ProjectEdit {
				return ports.SetProjectMaxPromptTokens{Value: val}
			},
		},
		{
			id: 8, label: "Max Steps:          ", isChoice: true,
			f:           m.choice("Max Steps:          ", []string{"default", "20", "50", "100", "unlimited (0)"}, p.MaxSteps),
			configKey:   "chat.max_steps",
			description: "Interactive turn step ceiling for the agent loop. 0 means unlimited.",
			apply: func(val string) ports.ProjectEdit {
				return ports.SetProjectMaxSteps{Value: val}
			},
		},
	}
}

func buildToolStorageRows(p ports.ProjectView, m projectFieldMaker) []projectRow {
	sandboxChoice := "on"
	if !p.Sandbox {
		sandboxChoice = "off"
	}
	redactChoice := "off"
	if p.RedactToolArgs {
		redactChoice = "on"
	}

	return []projectRow{
		{
			id: 9, label: "Run Timeout:        ", isChoice: true,
			f:           m.choice("Run Timeout:        ", []string{"300", "600", "900", "1800", "3600"}, strconv.Itoa(p.RunTimeoutSec)),
			configKey:   "tools.run_timeout_seconds",
			description: "Maximum execution timeout in seconds for run_command invocations.",
			apply: func(val string) ports.ProjectEdit {
				n, _ := strconv.Atoi(val)
				return ports.SetProjectRunTimeout{Seconds: n}
			},
		},
		{
			id: 10, label: "Storage Backend:    ", isChoice: true,
			f:           m.choice("Storage Backend:    ", []string{"sqlite", "memory"}, p.StoreBackend),
			configKey:   "subagents.store_backend",
			description: "Durable storage backend for sessions, context, and runs (sqlite or memory).",
			apply: func(val string) ports.ProjectEdit {
				return ports.SetProjectStoreBackend{Backend: val}
			},
		},
		{
			id: 11, label: "Storage Path:       ", isChoice: false,
			f:           m.text("Storage Path:       ", p.StorePath),
			configKey:   "subagents.store_path",
			description: "SQLite database file path (leave empty to use default .mivia/context.db).",
			apply: func(val string) ports.ProjectEdit {
				return ports.SetProjectStorePath{Path: strings.TrimSpace(val)}
			},
		},
		{
			id: 12, label: "Harness Sandbox:    ", isChoice: true,
			f:           m.choice("Harness Sandbox:    ", []string{"on", "off"}, sandboxChoice),
			configKey:   "harness.sandbox",
			description: "Bubblewrap (bwrap) isolation for workflow verifier and evidence gate commands.",
			apply: func(val string) ports.ProjectEdit {
				return ports.SetProjectSandbox{On: val == "on"}
			},
		},
		{
			id: 13, label: "Redact Tool Args:   ", isChoice: true,
			f:           m.choice("Redact Tool Args:   ", []string{"on", "off"}, redactChoice),
			configKey:   "privacy.redact_tool_args",
			description: "Hides command-line arguments in run_command outputs from operator previews.",
			apply: func(val string) ports.ProjectEdit {
				return ports.SetProjectRedactToolArgs{On: val == "on"}
			},
		},
	}
}

type projectsSavedMsg struct{}
type projectsFailedMsg struct{ message string }

func awaitProjectsSave(h ports.SaveHandle) tea.Cmd {
	if h == nil {
		return nil
	}
	return func() tea.Msg {
		for ev := range h.Events() {
			if ev.State == ports.SaveFailed {
				return projectsFailedMsg{message: ev.Message}
			}
		}
		return projectsSavedMsg{}
	}
}

func (s *projectsSection) Update(msg tea.Msg) (section, tea.Cmd) {
	switch msg := msg.(type) {
	case projectsSavedMsg:
		s.notice = ""
		s.rebuild()
		return s, nil
	case projectsFailedMsg:
		s.notice = msg.message
		return s, nil
	case tea.KeyPressMsg:
		return s.handleKey(msg)
	}
	return s, nil
}

func (s *projectsSection) handleKey(msg tea.KeyPressMsg) (section, tea.Cmd) {
	if s.store == nil {
		return s, nil
	}

	if s.editing {
		return s.handleEditorKey(msg)
	}

	if len(s.rows) == 0 {
		return s, nil
	}

	switch msg.String() {
	case "up", "k":
		if s.cursor > 0 {
			s.cursor--
		}
		s.notice = ""
	case "down", "j":
		if s.cursor < len(s.rows)-1 {
			s.cursor++
		}
		s.notice = ""
	case "n":
		s.notice = "project creation is not available; configure projects in mivia.toml"
		return s, nil
	case "x":
		s.notice = "project deletion is not available; configure projects in mivia.toml"
		return s, nil
	case " ", "space":
		return s.activateCurrentRow(true)
	case "enter", "e":
		return s.activateCurrentRow(false)
	}
	return s, nil
}

func (s *projectsSection) activateCurrentRow(isSpace bool) (section, tea.Cmd) {
	if s.cursor < 0 || s.cursor >= len(s.rows) {
		return s, nil
	}
	row := &s.rows[s.cursor]
	if row.isReadOnly {
		s.notice = "this field is read-only (set by workspace environment)"
		return s, nil
	}

	if row.isChoice {
		row.f.Cycle(1)
		newVal := row.f.Value()
		if row.apply != nil {
			edit := row.apply(newVal)
			handle, err := s.store.Apply(context.Background(), ports.ScopeProject, edit)
			if err != nil {
				s.notice = err.Error()
				return s, nil
			}
			s.notice = ""
			return s, awaitProjectsSave(handle)
		}
		return s, nil
	}

	// Text field -> start inline editing
	s.editing = true
	s.editRowIndex = s.cursor
	s.editOrigVal = row.f.Value()
	s.notice = ""
	cmd := row.f.Focus()
	return s, cmd
}

func (s *projectsSection) handleEditorKey(msg tea.KeyPressMsg) (section, tea.Cmd) {
	if s.editRowIndex < 0 || s.editRowIndex >= len(s.rows) {
		s.editing = false
		return s, nil
	}
	row := &s.rows[s.editRowIndex]

	switch msg.String() {
	case "esc":
		row.f.SetValue(s.editOrigVal)
		row.f.Blur()
		s.editing = false
		s.notice = ""
		return s, nil
	case "enter", "ctrl+s":
		val := row.f.Value()
		if row.id == 3 { // Branch Prefix
			trimmed := strings.TrimSpace(val)
			if trimmed == "" || !strings.HasSuffix(trimmed, "/") {
				s.notice = "Branch prefix must end with / (e.g. mivia/)"
				return s, nil
			}
		}

		row.f.Blur()
		s.editing = false
		s.notice = ""
		if row.apply != nil {
			edit := row.apply(val)
			handle, err := s.store.Apply(context.Background(), ports.ScopeProject, edit)
			if err != nil {
				s.notice = err.Error()
				return s, nil
			}
			return s, awaitProjectsSave(handle)
		}
		return s, nil
	}

	var cmd tea.Cmd
	row.f, cmd = row.f.Update(msg)
	return s, cmd
}

func (s *projectsSection) Hints() []keymap.ID {
	if s.editing {
		return []keymap.ID{
			keymap.IDSettingsToggle,
			keymap.IDSettingsBack,
		}
	}
	return []keymap.ID{
		keymap.IDSettingsUp,
		keymap.IDSettingsDown,
		keymap.IDSettingsToggle,
	}
}
