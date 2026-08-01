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

// ResolveOptions controls inheritance and global guardrails.
type ResolveOptions struct {
	Global config.AgentsGlobal
	// KnownTools is the compiled catalogue (tools.AllToolNames). Required.
	KnownTools map[string]struct{}
	// SkillNames, when set, rejects agent names that collide with skills.
	SkillNames map[string]struct{}
	// ReservedHandlers rejects agent names that collide with built-in handlers.
	ReservedHandlers map[string]struct{}
	// SkillCatalogue maps skill name → dual-origin presence. When set, agent
	// skills allowlists are validated against it (plan 06).
	SkillCatalogue map[string]SkillCatalogueEntry
	// AllowProjectSkills enables project-origin skills in agent allowlists.
	// Mirrors the workspace gate (load_workspace_config).
	AllowProjectSkills bool
}

// ResolveAll resolves every input into immutable ResolvedAgent values and
// publishes them to a new AgentRegistry.
func ResolveAll(inputs []ResolveInput, opts ResolveOptions) (*AgentRegistry, []string, error) {
	if opts.KnownTools == nil {
		opts.KnownTools = knownToolSet(tools.AllToolNames())
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
		if ia.Source != ib.Source {
			if ia.Source == config.AgentSourceUser {
				return -1
			}
			if ib.Source == config.AgentSourceUser {
				return 1
			}
		}
		return strings.Compare(a, b)
	})
	return order
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
	toolsList    *[]string
	disallowed   *[]string
	skills       *[]string
	model        string
	maxTurns     *int
	systemPrompt string
}

func materialize(in ResolveInput, parent *ResolvedAgent, parentName string, opts ResolveOptions) (ResolvedAgent, []string, error) {
	fields := inheritFields(in.Spec, parent, opts)
	fields.toolsList = applyToolDeltas(fields.toolsList, in.Spec)
	fields.toolsList = defaultToolPool(fields.toolsList, opts)

	dis := []string{}
	if fields.disallowed != nil {
		dis = slices.Clone(*fields.disallowed)
	}
	effective, err := applyToolPolicy(*fields.toolsList, dis, opts)
	if err != nil {
		return ResolvedAgent{}, nil, fmt.Errorf("agent %q: %w", in.Name, err)
	}
	if len(effective) == 0 && opts.Global.FailOnEmptyToolset {
		return ResolvedAgent{}, nil, fmt.Errorf("agent %q: empty toolset refused (fail_on_empty_toolset)", in.Name)
	}
	if err := validateCatalogueTools(in.Name, effective, opts.KnownTools); err != nil {
		return ResolvedAgent{}, nil, err
	}
	skills, origins, err := resolveSkillsAllowlist(in.Name, fields.skills, opts)
	if err != nil {
		return ResolvedAgent{}, nil, err
	}
	desc := ""
	if in.Spec.Description != nil {
		desc = *in.Spec.Description
	}
	return ResolvedAgent{
		Name:            in.Name,
		Description:     SanitizeDescription(desc),
		Model:           fields.model,
		MaxTurns:        fields.maxTurns,
		SystemPrompt:    fields.systemPrompt,
		EffectiveTools:  effective,
		DisallowedTools: dis,
		Skills:          skills,
		SkillOrigins:    origins,
		Provenance:      Provenance{Source: in.Source, Path: in.Path},
		ParentName:      parentName,
	}, nil, nil
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
		f.model = parent.Model
		if parent.MaxTurns != nil {
			v := *parent.MaxTurns
			f.maxTurns = &v
		}
		f.systemPrompt = parent.SystemPrompt
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
	if spec.Model != nil {
		f.model = strings.TrimSpace(*spec.Model)
	}
	if spec.MaxTurns != nil {
		v := *spec.MaxTurns
		f.maxTurns = &v
	}
	if spec.SystemPrompt != nil {
		f.systemPrompt = *spec.SystemPrompt
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
	all := tools.AllToolNames()
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
	return nil
}

func knownToolSet(names []string) map[string]struct{} {
	out := make(map[string]struct{}, len(names))
	for _, n := range names {
		out[n] = struct{}{}
	}
	return out
}

// orderedSet preserves first-seen order for tool allowlists.
type orderedSet struct {
	order []string
	set   map[string]struct{}
}

func newOrderedSet(init []string) *orderedSet {
	o := &orderedSet{set: make(map[string]struct{})}
	for _, n := range init {
		o.add(n)
	}
	return o
}

func (o *orderedSet) add(n string) {
	n = strings.TrimSpace(n)
	if n == "" {
		return
	}
	if _, ok := o.set[n]; ok {
		return
	}
	o.set[n] = struct{}{}
	o.order = append(o.order, n)
}

func (o *orderedSet) remove(n string) {
	n = strings.TrimSpace(n)
	if _, ok := o.set[n]; !ok {
		return
	}
	delete(o.set, n)
	out := o.order[:0]
	for _, x := range o.order {
		if x != n {
			out = append(out, x)
		}
	}
	o.order = out
}

func (o *orderedSet) slice() []string {
	return slices.Clone(o.order)
}
