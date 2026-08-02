package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/MiviaLabs/mivia-agent/internal/providerregistry"
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
	Name            *string         `toml:"name"`
	Description     *string         `toml:"description"`
	Inherits        *string         `toml:"inherits"`
	Tools           *[]string       `toml:"tools"`
	ToolsAdd        *[]string       `toml:"tools_add"`
	ToolsRemove     *[]string       `toml:"tools_remove"`
	DisallowedTools *[]string       `toml:"disallowed_tools"`
	Skills          *[]string       `toml:"skills"`
	Provider        *string         `toml:"provider"`
	Model           *string         `toml:"model"`
	MaxTurns        *int            `toml:"max_turns"`
	TimeoutSeconds  *int            `toml:"timeout_seconds"`
	MaxTokens       *int            `toml:"max_tokens"`
	SystemPrompt    *string         `toml:"system_prompt"`
	OutputSchema    *map[string]any `toml:"output_schema"`
	InputSchema     *map[string]any `toml:"input_schema"`
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
		Provider:        normalizeProviderRef(r.Provider),
		Model:           r.Model,
		MaxTurns:        r.MaxTurns,
		TimeoutSeconds:  r.TimeoutSeconds,
		MaxTokens:       r.MaxTokens,
		SystemPrompt:    r.SystemPrompt,
		OutputSchema:    r.OutputSchema,
		InputSchema:     r.InputSchema,
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
	if spec.Model != nil {
		model := strings.TrimSpace(*spec.Model)
		if len(model) > 200 || strings.IndexFunc(model, unicode.IsControl) >= 0 {
			return fmt.Errorf("model is invalid")
		}
	}
	if err := validateAgentProvider(spec); err != nil {
		return err
	}
	// A ceiling that is present must actually bound something. Unlike
	// max_turns, zero is not an "unlimited" sentinel here: an agent with
	// unlimited turns and no wall-clock or token ceiling is exactly the
	// unbounded-spend case these keys exist to prevent, so an explicit 0 is a
	// mistake rather than a policy.
	for _, ceiling := range []struct {
		key   string
		value *int
	}{{"timeout_seconds", spec.TimeoutSeconds}, {"max_tokens", spec.MaxTokens}} {
		if ceiling.value != nil && *ceiling.value <= 0 {
			return fmt.Errorf("%s must be > 0 when set", ceiling.key)
		}
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

// normalizeProviderRef lowercases and trims an authored provider name so that
// catalog matching, provider construction, and the agent definition digest all
// agree on one spelling. A present-but-blank value is preserved (not folded to
// nil) so validateAgentProvider can reject it distinctly from an omitted key.
func normalizeProviderRef(name *string) *string {
	if name == nil {
		return nil
	}
	normalized := strings.ToLower(strings.TrimSpace(*name))
	return &normalized
}

// validateAgentProvider enforces the authored half of the provider/model
// binding. The name check is a spelling check against the built-in
// descriptors, not the fail-closed authorization gate: whether a provider is
// actually configured and holds a credential is decided later, by
// provider.NewForProvider. Cross-provider ambiguity is what is settled here.
func validateAgentProvider(spec AgentFileSpec) error {
	if spec.Provider == nil {
		return nil
	}
	name := *spec.Provider
	if name == "" {
		return fmt.Errorf("provider must not be empty when set")
	}
	if _, known := providerregistry.Lookup(name); !known {
		return fmt.Errorf("provider %q is not a known provider (available: %s)",
			name, strings.Join(providerregistry.Names(), ", "))
	}
	// A provider with no model would pair a foreign endpoint with whatever
	// model the session happens to hold - the exact ambiguity an explicit
	// binding exists to remove.
	if spec.Model == nil || strings.TrimSpace(*spec.Model) == "" {
		return fmt.Errorf("provider %q requires model to be set on the same agent", name)
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
