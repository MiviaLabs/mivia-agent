package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/pelletier/go-toml/v2"
)

// ParseAgentFileTOML parses a single agent definition body with unknown-key
// rejection and presence-preserving optional fields. filename is the base
// name used for name agreement (e.g. "researcher.toml").
func ParseAgentFileTOML(data []byte, filename string) (AgentFileSpec, string, error) {
	canonical, err := agentNameFromFilename(filename)
	if err != nil {
		return AgentFileSpec{}, "", err
	}
	var raw agentFileTOML
	dec := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return AgentFileSpec{}, "", fmt.Errorf("agent %q: %w", canonical, err)
	}
	spec := raw.toSpec()
	if spec.Name == nil || strings.TrimSpace(*spec.Name) == "" {
		return AgentFileSpec{}, "", fmt.Errorf("agent %q: name is required", canonical)
	}
	if *spec.Name != canonical {
		return AgentFileSpec{}, "", fmt.Errorf(
			"agent %q: in-file name %q does not match filename", canonical, *spec.Name)
	}
	if err := validateAgentFileSpec(spec); err != nil {
		return AgentFileSpec{}, "", fmt.Errorf("agent %q: %w", canonical, err)
	}
	return spec, canonical, nil
}

// agentFileTOML is the on-disk shape. skills is the invocation allowlist
// (plan 06); enforcement lives in internal/agents + internal/cli.
type agentFileTOML struct {
	Name            *string   `toml:"name"`
	Description     *string   `toml:"description"`
	Inherits        *string   `toml:"inherits"`
	Tools           *[]string `toml:"tools"`
	ToolsAdd        *[]string `toml:"tools_add"`
	ToolsRemove     *[]string `toml:"tools_remove"`
	DisallowedTools *[]string `toml:"disallowed_tools"`
	Skills          *[]string `toml:"skills"`
	Model           *string   `toml:"model"`
	MaxTurns        *int      `toml:"max_turns"`
	SystemPrompt    *string   `toml:"system_prompt"`
}

func (r agentFileTOML) toSpec() AgentFileSpec {
	return AgentFileSpec{
		Name:            r.Name,
		Description:     r.Description,
		Inherits:        r.Inherits,
		Tools:           r.Tools,
		ToolsAdd:        r.ToolsAdd,
		ToolsRemove:     r.ToolsRemove,
		DisallowedTools: r.DisallowedTools,
		Skills:          r.Skills,
		Model:           r.Model,
		MaxTurns:        r.MaxTurns,
		SystemPrompt:    r.SystemPrompt,
	}
}

func validateAgentFileSpec(spec AgentFileSpec) error {
	if spec.Name != nil {
		if err := validateAgentName(*spec.Name); err != nil {
			return err
		}
	}
	if spec.Description != nil && strings.TrimSpace(*spec.Description) == "" {
		return fmt.Errorf("description must not be empty when set")
	}
	if spec.SystemPrompt != nil && *spec.SystemPrompt == "" {
		return fmt.Errorf("system_prompt must not be empty when set")
	}
	if spec.Model != nil && strings.TrimSpace(*spec.Model) == "" {
		return fmt.Errorf("model must not be empty when set")
	}
	// max_turns: omit = unset (caller/session default); 0 = unlimited; >0 = cap.
	if spec.MaxTurns != nil && *spec.MaxTurns < 0 {
		return fmt.Errorf("max_turns must be >= 0 (0 means unlimited)")
	}
	hasTools := spec.Tools != nil
	hasAdd := spec.ToolsAdd != nil
	hasRemove := spec.ToolsRemove != nil
	if hasTools && (hasAdd || hasRemove) {
		return fmt.Errorf("tools is mutually exclusive with tools_add/tools_remove; remove tools_add/tools_remove if stating a full tools list, or remove tools to extend an inherited pool with tools_add/tools_remove")
	}
	return nil
}

func agentNameFromFilename(filename string) (string, error) {
	base := filepath.Base(filename)
	if !strings.HasSuffix(base, ".toml") {
		return "", fmt.Errorf("agent file %q must end in .toml", base)
	}
	name := strings.TrimSuffix(base, ".toml")
	if err := validateAgentName(name); err != nil {
		return "", err
	}
	return name, nil
}

func validateAgentName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("agent name is empty")
	}
	if strings.ContainsAny(name, `/\:`) || strings.Contains(name, "..") {
		return fmt.Errorf("agent name %q is invalid", name)
	}
	if name != strings.ToLower(name) {
		return fmt.Errorf("agent name %q must be lowercase", name)
	}
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' {
			continue
		}
		return fmt.Errorf("agent name %q contains invalid character %q", name, r)
	}
	return nil
}

// agentsSectionTOML is the [agents] fragment of mivia.toml.
type agentsSectionTOML struct {
	LoadWorkspaceConfig *bool `toml:"load_workspace_config"`
	Guardrails          *struct {
		MandatoryToolDenylist *[]string `toml:"mandatory_tool_denylist"`
		RequireExplicitTools  *bool     `toml:"require_explicit_tools"`
		FailOnEmptyToolset    *bool     `toml:"fail_on_empty_toolset"`
	} `toml:"guardrails"`
}

type agentsFileTOML struct {
	Agents *agentsSectionTOML `toml:"agents"`
}

func readAgentsSection(path string) (agentsSectionTOML, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return agentsSectionTOML{}, err
	}
	var file agentsFileTOML
	if err := toml.Unmarshal(data, &file); err != nil {
		return agentsSectionTOML{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	if file.Agents == nil {
		return agentsSectionTOML{}, nil
	}
	return *file.Agents, nil
}

func applyAgentsSection(g *AgentsGlobal, section agentsSectionTOML) {
	if section.LoadWorkspaceConfig != nil {
		g.LoadWorkspaceConfig = *section.LoadWorkspaceConfig
	}
	if section.Guardrails == nil {
		return
	}
	gr := section.Guardrails
	if gr.RequireExplicitTools != nil {
		g.RequireExplicitTools = *gr.RequireExplicitTools
	}
	if gr.FailOnEmptyToolset != nil {
		g.FailOnEmptyToolset = *gr.FailOnEmptyToolset
	}
	if gr.MandatoryToolDenylist != nil {
		g.MandatoryToolDenylistAdditions = append([]string(nil), (*gr.MandatoryToolDenylist)...)
	}
}

func hasAgentsTable(data []byte) bool {
	var file agentsFileTOML
	if err := toml.Unmarshal(data, &file); err != nil {
		return false
	}
	return file.Agents != nil
}
