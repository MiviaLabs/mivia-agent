package uiadapter

import (
	"context"
	"fmt"
	"strconv"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

type settingsProjects struct{ *SettingsStore }

func (s *SettingsStore) initProjectsFromConfig() {
	wsPath := ""
	if s.agentState != nil {
		wsPath = s.agentState.WorkspaceRoot
	}
	cfgPath := s.projectConfigPath()
	if cfgPath == "" && wsPath != "" {
		cfgPath = config.ProjectConfigPath(wsPath)
	}
	if cfgPath == "" && s.res != nil && s.res.ConfigPath != "" {
		cfgPath = s.res.ConfigPath
	}

	p := ports.ProjectView{
		WorkspacePath:   wsPath,
		ConfigPath:      cfgPath,
		EnvFile:         "./.env",
		BranchPrefix:    "mivia/",
		SystemPrompt:    "You are mivia, a local CLI coding agent.",
		Temperature:     "default",
		MaxTokens:       "default",
		MaxPromptTokens: "default",
		MaxSteps:        "default",
		RunTimeoutSec:   900,
		StoreBackend:    "sqlite",
		StorePath:       "",
		Sandbox:         true,
		RedactToolArgs:  false,
	}

	if s.res != nil {
		applyResolvedDefaults(&p, s.res)
	}

	s.project = p
}

func applyResolvedDefaults(p *ports.ProjectView, res *config.Resolved) {
	if res.EnvFilePath != "" {
		p.EnvFile = res.EnvFilePath
	}
	if res.Worktrees.BranchPrefix != "" {
		p.BranchPrefix = res.Worktrees.BranchPrefix
	}
	if res.SystemPrompt != "" {
		p.SystemPrompt = res.SystemPrompt
	}
	if res.Temperature != nil {
		p.Temperature = fmt.Sprintf("%.1f", *res.Temperature)
	}
	if res.MaxTokens != nil {
		p.MaxTokens = strconv.Itoa(*res.MaxTokens)
	}
	if res.MaxPromptTokens != nil {
		p.MaxPromptTokens = strconv.Itoa(*res.MaxPromptTokens)
	}
	if res.MaxSteps != nil {
		if *res.MaxSteps == 0 {
			p.MaxSteps = "unlimited (0)"
		} else {
			p.MaxSteps = strconv.Itoa(*res.MaxSteps)
		}
	}
	if res.Tools.RunTimeoutSec > 0 {
		p.RunTimeoutSec = res.Tools.RunTimeoutSec
	}
	if res.Subagents.StoreBackend != "" {
		p.StoreBackend = res.Subagents.StoreBackend
	}
	if res.Subagents.StorePath != "" {
		p.StorePath = res.Subagents.StorePath
	}
	if res.Harness.Sandbox != nil {
		p.Sandbox = *res.Harness.Sandbox
	}
	p.RedactToolArgs = res.Privacy.RedactToolArgs
}

func (p settingsProjects) Project() ports.ProjectView {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.project
}

func (p settingsProjects) Apply(ctx context.Context, scope ports.Scope, e ports.ProjectEdit) (ports.SaveHandle, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	field, err := applyProjectFieldEdit(&p.project, e)
	if err != nil {
		return nil, err
	}

	targetPath := p.projectConfigPath()
	if targetPath == "" {
		if p.project.ConfigPath != "" {
			targetPath = p.project.ConfigPath
		} else {
			targetPath = p.configPath()
		}
	}

	if targetPath != "" {
		ps := config.ProjectSettings{
			EnvFile:         p.project.EnvFile,
			BranchPrefix:    p.project.BranchPrefix,
			SystemPrompt:    p.project.SystemPrompt,
			Temperature:     p.project.Temperature,
			MaxTokens:       p.project.MaxTokens,
			MaxPromptTokens: p.project.MaxPromptTokens,
			MaxSteps:        p.project.MaxSteps,
			RunTimeoutSec:   p.project.RunTimeoutSec,
			StoreBackend:    p.project.StoreBackend,
			StorePath:       p.project.StorePath,
			Sandbox:         p.project.Sandbox,
			RedactToolArgs:  p.project.RedactToolArgs,
		}
		if err := config.UpdateProjectConfig(targetPath, ps); err != nil {
			return nil, fmt.Errorf("persist project settings: %w", err)
		}
	}

	p.saveSeq++
	h := &saveHandle{
		id:     fmt.Sprintf("save-%d", p.saveSeq),
		events: make(chan ports.SaveEvent, 4),
		cancel: func() {},
	}

	go func(seq uint64, f string) {
		defer close(h.events)
		h.events <- ports.SaveEvent{State: ports.SavePending, Field: f, Message: "queued"}
		h.events <- ports.SaveEvent{State: ports.SaveValidating, Field: f, Message: "validating"}
		h.events <- ports.SaveEvent{State: ports.SaveSaved, Field: f, Message: fmt.Sprintf("saved %s", f)}
	}(p.saveSeq, field)

	return h, nil
}

func applyProjectFieldEdit(p *ports.ProjectView, e ports.ProjectEdit) (string, error) {
	switch v := e.(type) {
	case ports.SetProjectEnvFile:
		p.EnvFile = v.Path
		return "env_file", nil
	case ports.SetProjectBranchPrefix:
		p.BranchPrefix = v.Prefix
		return "branch_prefix", nil
	case ports.SetProjectSystemPrompt:
		p.SystemPrompt = v.Prompt
		return "system_prompt", nil
	case ports.SetProjectTemperature:
		p.Temperature = v.Value
		return "temperature", nil
	case ports.SetProjectMaxTokens:
		p.MaxTokens = v.Value
		return "max_tokens", nil
	case ports.SetProjectMaxPromptTokens:
		p.MaxPromptTokens = v.Value
		return "max_prompt_tokens", nil
	case ports.SetProjectMaxSteps:
		p.MaxSteps = v.Value
		return "max_steps", nil
	case ports.SetProjectRunTimeout:
		p.RunTimeoutSec = v.Seconds
		return "run_timeout_seconds", nil
	case ports.SetProjectStoreBackend:
		p.StoreBackend = v.Backend
		return "store_backend", nil
	case ports.SetProjectStorePath:
		p.StorePath = v.Path
		return "store_path", nil
	case ports.SetProjectSandbox:
		p.Sandbox = v.On
		return "sandbox", nil
	case ports.SetProjectRedactToolArgs:
		p.RedactToolArgs = v.On
		return "redact_tool_args", nil
	default:
		return "", fmt.Errorf("unknown project edit %T", e)
	}
}
