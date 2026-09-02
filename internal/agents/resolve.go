package agents

import (
	"fmt"
	"slices"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// ResolveInput is one file-backed agent prior to inheritance resolution.
type ResolveInput struct {
	Name   string
	Source config.AgentSource
	Path   string
	Spec   config.AgentFileSpec
}

// SkillCatalogueEntry describes one discoverable skill for allowlist resolution.
type SkillCatalogueEntry struct {
	// User is true when a user-origin skill of this name exists.
	User bool
	// Project is true when a project/workspace skill of this name exists.
	Project bool
}

// ResolveAll resolves every input into immutable ResolvedAgent values and
// publishes them to a new AgentRegistry.
func ResolveAll(inputs []ResolveInput, opts ResolveOptions) (*AgentRegistry, []string, error) {
	if opts.KnownTools == nil {
		opts.KnownTools = knownToolSet(tools.DeclaredToolNames())
	}
	byName, err := indexInputs(inputs)
	if err != nil {
		return nil, nil, err
	}
	state := &resolveState{
		byName:   byName,
		opts:     opts,
		resolved: make(map[string]ResolvedAgent, len(inputs)),
		visiting: make(map[string]bool),
	}
	reg := NewRegistry()
	for _, name := range orderedNames(byName) {
		agent, err := state.resolveOne(name)
		if err != nil {
			if opts.TolerantWorkspace && byName[name].Source != config.AgentSourceUser {
				// The trusted user boundary stays fail-closed; workspace and
				// compiled built-in inputs are tolerated with a skip warning.
				prefix := "skipped workspace agent"
				if byName[name].Source == config.AgentSourceBuiltIn {
					prefix = "skipped built-in agent"
				}
				state.warnings = append(state.warnings, fmt.Sprintf("%s %q: %s", prefix, name, err.Error()))
				continue
			}
			return nil, nil, err
		}
		if err := reg.Publish(agent); err != nil {
			return nil, nil, err
		}
	}
	return reg, state.warnings, nil
}

type resolveState struct {
	byName   map[string]ResolveInput
	opts     ResolveOptions
	resolved map[string]ResolvedAgent
	visiting map[string]bool
	warnings []string
}

func indexInputs(inputs []ResolveInput) (map[string]ResolveInput, error) {
	byName := make(map[string]ResolveInput, len(inputs))
	for _, in := range inputs {
		if _, dup := byName[in.Name]; dup {
			return nil, fmt.Errorf("duplicate agent name %q", in.Name)
		}
		byName[in.Name] = in
	}
	return byName, nil
}

func orderedNames(byName map[string]ResolveInput) []string {
	order := make([]string, 0, len(byName))
	for name := range byName {
		order = append(order, name)
	}
	slices.SortStableFunc(order, func(a, b string) int {
		ia, ib := byName[a], byName[b]
		if ra, rb := sourceRank(ia.Source), sourceRank(ib.Source); ra != rb {
			return ra - rb
		}
		return strings.Compare(a, b)
	})
	return order
}

// sourceRank orders resolution and publication: user definitions first, then
// workspace, then compiled built-ins (built-ins publish after every
// file-backed agent).
func sourceRank(s config.AgentSource) int {
	switch s {
	case config.AgentSourceUser:
		return 0
	case config.AgentSourceWorkspace:
		return 1
	default:
		return 2
	}
}

func (s *resolveState) resolveOne(name string) (ResolvedAgent, error) {
	if a, ok := s.resolved[name]; ok {
		return a, nil
	}
	in, ok := s.byName[name]
	if !ok {
		return ResolvedAgent{}, fmt.Errorf("agent %q: unknown parent %q (only file-backed agents may be inherited)", name, name)
	}
	if s.visiting[name] {
		return ResolvedAgent{}, fmt.Errorf("agent %q: inheritance cycle", name)
	}
	s.visiting[name] = true
	defer delete(s.visiting, name)

	if err := checkNameCollisions(name, in.Path, s.opts); err != nil {
		return ResolvedAgent{}, err
	}
	parentName, parent, err := s.resolveParent(in)
	if err != nil {
		return ResolvedAgent{}, err
	}
	agent, warn, err := materialize(in, parent, parentName, s.opts)
	if err != nil {
		return ResolvedAgent{}, err
	}
	s.warnings = append(s.warnings, warn...)
	s.resolved[name] = agent
	return agent, nil
}

func (s *resolveState) resolveParent(in ResolveInput) (string, *ResolvedAgent, error) {
	if in.Spec.Inherits == nil {
		return "", nil, nil
	}
	p := strings.TrimSpace(*in.Spec.Inherits)
	if p == "" {
		return "", nil, fmt.Errorf("agent %q: inherits must not be empty when set", in.Name)
	}
	if p == "default" || p == "root" || p == "_root" {
		return "", nil, fmt.Errorf("agent %q: inherits %q is not a selectable or inheritable parent (file-backed agents only)", in.Name, p)
	}
	pa, err := s.resolveOne(p)
	if err != nil {
		if strings.Contains(err.Error(), "unknown parent") {
			return "", nil, fmt.Errorf("agent %q: unknown parent %q", in.Name, p)
		}
		return "", nil, fmt.Errorf("agent %q: %w", in.Name, err)
	}
	if err := checkInheritanceSourceBoundary(in, pa, s.opts.Global); err != nil {
		return "", nil, err
	}
	return p, &pa, nil
}

type inheritedFields struct {
	toolsList      *[]string
	disallowed     *[]string
	skills         *[]string
	coreTools      *[]string
	provider       string
	model          string
	maxTurns       *int
	timeoutSeconds *int
	maxTokens      *int
	systemPrompt   string
	outputSchema   map[string]any
	inputSchema    map[string]any
}

// effectiveDenylistFor is the set applyToolPolicy denies with, kept on the
// resolved agent for producers that add tool names AFTER it has run.
//
// cliagents.AuthorizedAgentTools is the one that matters: it grants authority
// over every tool of a selected MCP server, which by definition is not in the
// agent file, and several of the surfaces it feeds cannot reach the operator's
// config to re-check. Carrying the denial in the immutable resolved snapshot
// is what makes it apply everywhere the agent goes.
func effectiveDenylistFor(disallowed []string, opts ResolveOptions) []string {
	return append(slices.Clone(disallowed), opts.Global.MandatoryToolDenylistAdditions...)
}

func materialize(in ResolveInput, parent *ResolvedAgent, parentName string, opts ResolveOptions) (ResolvedAgent, []string, error) {
	var warn []string
	fields := inheritFields(in.Spec, parent, opts)
	fields.toolsList = applyToolDeltas(fields.toolsList, in.Spec)
	fields.toolsList = defaultToolPool(fields.toolsList, opts)
	// tools_remove also opts out of baseline inject (post_message) via DisallowedTools.
	fields.disallowed = mergeToolsRemoveIntoDisallowed(fields.disallowed, in.Spec)

	dis := []string{}
	if fields.disallowed != nil {
		dis = slices.Clone(*fields.disallowed)
	}
	effective, err := applyToolPolicy(*fields.toolsList, dis, opts)
	if err != nil {
		return ResolvedAgent{}, nil, fmt.Errorf("agent %q: %w", in.Name, err)
	}
	allowEmptyTools := in.Spec.AllowEmptyTools != nil && *in.Spec.AllowEmptyTools && in.Spec.Tools != nil && len(*in.Spec.Tools) == 0
	if len(effective) == 0 && opts.Global.FailOnEmptyToolset && !allowEmptyTools {
		return ResolvedAgent{}, nil, fmt.Errorf("agent %q: empty toolset refused (fail_on_empty_toolset)", in.Name)
	}
	if err := validateCatalogueTools(in.Name, effective, opts.KnownTools); err != nil {
		return ResolvedAgent{}, nil, err
	}
	if err := checkResolvedBinding(in, fields); err != nil {
		return ResolvedAgent{}, nil, err
	}
	// Credential-routing protection (strip by default): a workspace-sourced
	// definition must not select a (provider, model) binding unless the
	// operator opted in through the user-only [agents] gate
	// AllowWorkspaceAgentProviders. checkResolvedBinding already validated the
	// authored pair above, so an unknown provider or a provider without a model
	// still fails closed from any origin; only the known, well-formed pair is
	// stripped here. Model without a provider is not a vector (it cannot name a
	// foreign endpoint) and is left alone.
	if warning, stripped := stripWorkspaceBinding(in.Name, in.Source, fields.provider, fields.model, opts); stripped {
		warn = append(warn, warning)
		fields.provider = ""
		fields.model = ""
	}
	skills, origins, err := resolveSkillsAllowlist(in.Name, fields.skills, opts)
	if err != nil {
		return ResolvedAgent{}, nil, err
	}
	mcpServers, err := resolveMCPServers(in, parent, opts.MCPConfig)
	if err != nil {
		return ResolvedAgent{}, nil, err
	}
	desc := ""
	if in.Spec.Description != nil {
		desc = *in.Spec.Description
	}
	baseline := []string{}
	if fields.toolsList != nil {
		baseline = slices.Clone(*fields.toolsList)
	}
	trace := buildTrace(in, parent, parentName, fields, baseline, effective, dis, skills, opts)
	return ResolvedAgent{
		Name:                in.Name,
		Description:         SanitizeDescription(desc),
		Provider:            fields.provider,
		Model:               fields.model,
		MaxTurns:            fields.maxTurns,
		TimeoutSeconds:      fields.timeoutSeconds,
		MaxTokens:           fields.maxTokens,
		SystemPrompt:        fields.systemPrompt,
		EffectiveTools:      effective,
		EffectiveMCPServers: mcpServers,
		AllowEmptyTools:     allowEmptyTools,
		DisallowedTools:     dis,
		EffectiveDenylist:   effectiveDenylistFor(dis, opts),
		CoreTools:           fields.coreTools,
		Skills:              skills,
		SkillOrigins:        origins,
		Provenance:          Provenance{Source: in.Source, Path: in.Path},
		ParentName:          parentName,
		Trace:               trace,
		OutputSchema:        fields.outputSchema,
		InputSchema:         fields.inputSchema,
	}, warn, nil
}

func inheritFields(spec config.AgentFileSpec, parent *ResolvedAgent, opts ResolveOptions) inheritedFields {
	var f inheritedFields
	if parent != nil {
		t := slices.Clone(parent.EffectiveTools)
		f.toolsList = &t
		d := slices.Clone(parent.DisallowedTools)
		f.disallowed = &d
		if parent.Skills != nil {
			s := slices.Clone(*parent.Skills)
			f.skills = &s
		}
		if parent.CoreTools != nil {
			c := slices.Clone(*parent.CoreTools)
			f.coreTools = &c
		}
		f.provider = parent.Provider
		f.model = parent.Model
		if parent.MaxTurns != nil {
			v := *parent.MaxTurns
			f.maxTurns = &v
		}
		// Ceilings inherit and override individually: unlike the provider/model
		// pair they are not one unit, because each bounds a different resource.
		if parent.TimeoutSeconds != nil {
			v := *parent.TimeoutSeconds
			f.timeoutSeconds = &v
		}
		if parent.MaxTokens != nil {
			v := *parent.MaxTokens
			f.maxTokens = &v
		}
		f.systemPrompt = parent.SystemPrompt
		f.outputSchema = cloneAnyMap(parent.OutputSchema)
		f.inputSchema = cloneAnyMap(parent.InputSchema)
	}
	if spec.Tools != nil {
		t := slices.Clone(*spec.Tools)
		f.toolsList = &t
	}
	if spec.DisallowedTools != nil {
		d := slices.Clone(*spec.DisallowedTools)
		f.disallowed = &d
	}
	if spec.Skills != nil {
		s := slices.Clone(*spec.Skills)
		f.skills = &s
	}
	if spec.ToolsCore != nil {
		c := slices.Clone(*spec.ToolsCore)
		f.coreTools = &c
	}
	f.provider, f.model = inheritBinding(spec, f.provider, f.model)
	if spec.MaxTurns != nil {
		v := *spec.MaxTurns
		f.maxTurns = &v
	}
	if spec.TimeoutSeconds != nil {
		v := *spec.TimeoutSeconds
		f.timeoutSeconds = &v
	}
	if spec.MaxTokens != nil {
		v := *spec.MaxTokens
		f.maxTokens = &v
	}
	if spec.SystemPrompt != nil {
		f.systemPrompt = *spec.SystemPrompt
	}
	if spec.OutputSchema != nil {
		f.outputSchema = cloneAnyMap(*spec.OutputSchema)
	}
	if spec.InputSchema != nil {
		f.inputSchema = cloneAnyMap(*spec.InputSchema)
	}
	if opts.Global.RequireExplicitTools && spec.Tools == nil && parent == nil && spec.ToolsAdd == nil {
		empty := []string{}
		f.toolsList = &empty
	}
	return f
}

// resolveSkillsAllowlist applies plan 06 nil/empty/explicit semantics and trust.
// nil → unrestricted (all trusted skills); empty → none; names → validated set.
func resolveSkillsAllowlist(agentName string, skills *[]string, opts ResolveOptions) (*[]string, map[string]string, error) {
	if skills == nil {
		return nil, nil, nil
	}
	if len(*skills) == 0 {
		empty := []string{}
		return &empty, map[string]string{}, nil
	}
	// When a catalogue is provided, every name must resolve to a trusted origin.
	// Without a catalogue (unit tests that only care about tool inheritance),
	// accept names as-is so existing resolve tests stay focused.
	out := make([]string, 0, len(*skills))
	origins := make(map[string]string, len(*skills))
	seen := make(map[string]struct{}, len(*skills))
	for _, raw := range *skills {
		name := strings.TrimSpace(raw)
		if name == "" {
			return nil, nil, fmt.Errorf("agent %q: skills entry must not be empty", agentName)
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		if opts.SkillCatalogue != nil {
			entry, ok := opts.SkillCatalogue[name]
			if !ok || (!entry.User && !entry.Project) {
				return nil, nil, fmt.Errorf("agent %q: unknown skill %q", agentName, name)
			}
			origin, err := pickSkillOrigin(agentName, name, entry, opts.AllowProjectSkills)
			if err != nil {
				return nil, nil, err
			}
			origins[name] = origin
		}
		out = append(out, name)
	}
	return &out, origins, nil
}

// pickSkillOrigin prefers user skills over project so a workspace skill cannot
// silently shadow a user binding. Project-only skills require the workspace gate.
func pickSkillOrigin(agentName, skillName string, entry SkillCatalogueEntry, allowProject bool) (string, error) {
	if entry.User {
		return string(config.AgentSourceUser), nil
	}
	if entry.Project {
		if !allowProject {
			return "", fmt.Errorf("agent %q: skill %q is workspace-only; enable load_workspace_config to use project skills", agentName, skillName)
		}
		return string(config.AgentSourceWorkspace), nil
	}
	return "", fmt.Errorf("agent %q: unknown skill %q", agentName, skillName)
}

func applyToolDeltas(toolsList *[]string, spec config.AgentFileSpec) *[]string {
	if spec.ToolsAdd == nil && spec.ToolsRemove == nil {
		return toolsList
	}
	var base []string
	if toolsList != nil {
		base = slices.Clone(*toolsList)
	}
	set := newOrderedSet(base)
	if spec.ToolsAdd != nil {
		for _, n := range *spec.ToolsAdd {
			set.add(n)
		}
	}
	if spec.ToolsRemove != nil {
		for _, n := range *spec.ToolsRemove {
			set.remove(n)
		}
	}
	list := set.slice()
	return &list
}

func defaultToolPool(toolsList *[]string, opts ResolveOptions) *[]string {
	if toolsList != nil {
		return toolsList
	}
	if opts.Global.RequireExplicitTools {
		empty := []string{}
		return &empty
	}
	all := tools.DeclaredToolNames()
	return &all
}

func validateCatalogueTools(agentName string, effective []string, known map[string]struct{}) error {
	for _, n := range effective {
		if _, ok := known[n]; !ok {
			return fmt.Errorf("agent %q: unknown tool %q (not in catalogue)", agentName, n)
		}
	}
	return nil
}

func checkInheritanceSourceBoundary(child ResolveInput, parent ResolvedAgent, _ config.AgentsGlobal) error {
	// Inheritance is only between file-backed definitions of the SAME trust
	// origin (plan 05 phase 03: source-boundary violations fail closed). A
	// workspace definition must not be able to inject its prompt/tools/skills
	// into a user-trusted definition (INV-AG-29: workspace configuration
	// cannot inject untrusted content into gated prompt surfaces or widen the
	// user's authorized tool set), and a workspace definition must not
	// silently absorb the user's trusted prompt and tool scope. Discovery
	// shadowing only protects same-named files; cross-name inheritance needs
	// this explicit gate.
	if child.Source != parent.Provenance.Source {
		return fmt.Errorf("agent %q: inheritance across source boundary (%s agent may not inherit %s agent %q)",
			child.Name, child.Source, parent.Provenance.Source, parent.Name)
	}
	return nil
}

func checkNameCollisions(name, path string, opts ResolveOptions) error {
	if opts.SkillNames != nil {
		if _, ok := opts.SkillNames[name]; ok {
			return fmt.Errorf("agent %q at %s collides with a skill of the same name", name, path)
		}
	}
	if opts.ReservedHandlers != nil {
		if _, ok := opts.ReservedHandlers[name]; ok {
			return fmt.Errorf("agent %q at %s collides with reserved handler %q", name, path, name)
		}
	}
	// The root identity is compiled, never a registry member, and selected by
	// name from the flag, /agent, and the picker. A file of this name would be
	// simultaneously spawnable and shadowed by the root special case, so the
	// name is reserved. No compiled built-in may carry it either: builtInInputs
	// never emits it, which is what keeps this check source-agnostic.
	if name == config.RootAgentName {
		return fmt.Errorf("agent %q at %s collides with the reserved root agent name", name, path)
	}
	return nil
}

func knownToolSet(names []string) map[string]struct{} {
	out := make(map[string]struct{}, len(names))
	for _, n := range names {
		out[n] = struct{}{}
	}
	return out
}

func stripWorkspaceBinding(name string, source config.AgentSource, provider, model string, opts ResolveOptions) (string, bool) {
	if source != config.AgentSourceWorkspace || provider == "" || opts.Global.AllowWorkspaceAgentProviders {
		return "", false
	}
	dropped := fmt.Sprintf("provider %q", provider)
	if model != "" {
		dropped = fmt.Sprintf("provider %q and model %q", provider, model)
	}
	return fmt.Sprintf(
		"agent %q: workspace-declared %s ignored (credential-routing protection); "+
			"agent runs the session provider and model. To honor it, set "+
			"allow_workspace_agent_providers = true under [agents] in your user config",
		name, dropped), true
}
