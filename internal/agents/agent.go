// Package agents resolves file-backed agent definitions into immutable
// runtime snapshots. It does not construct CLI dispatchers or registries.
package agents

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"sync"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// Provenance records where a resolved agent definition came from.
type Provenance struct {
	Source config.AgentSource
	Path   string
}

// AgentSpec is the presence-preserving authored definition (from config).
type AgentSpec = config.AgentFileSpec

// ResolvedAgent is an immutable published agent definition.
// After Publish, fields must not be mutated; clones are returned to callers.
type ResolvedAgent struct {
	Name        string
	Description string
	// Provider is the built-in provider owning Model. Empty means the agent
	// inherits the session's provider, which is the default and keeps Model
	// provider-local. Only a user-trusted definition may set it.
	Provider string
	Model    string
	MaxTurns *int // nil = unset
	// TimeoutSeconds and MaxTokens bound wall-clock time and per-response
	// provider spend independently of MaxTurns: max_turns = 0 means unlimited
	// iterations, not an unbounded run. nil = inherit the session's.
	TimeoutSeconds  *int
	MaxTokens       *int
	SystemPrompt    string
	EffectiveTools  []string // final allowlist after inheritance/deltas/guardrails
	DisallowedTools []string // effective denylist names applied before allowlist
	// Skills is the resolved skill invocation allowlist (plan 06).
	// nil = all trusted skills; non-nil empty = none; non-nil = named set only.
	Skills *[]string
	// SkillOrigins records the trusted origin for each explicitly allowed skill
	// name. Empty when Skills is nil (unrestricted).
	SkillOrigins map[string]string
	Provenance   Provenance
	// ParentName is the resolved parent, empty when none.
	ParentName string
	// Trace is the provider-independent resolution explanation used by catalog
	// inspection. It is never serialized into runtime events.
	Trace ResolutionTrace
	// DisabledTools are catalogue-known tools dropped because they are absent
	// from the position's registry (filled by Layer C; empty after resolve).
	DisabledTools []string
	// OutputSchema is the resolved JSON Schema for structured final replies
	// (plan tools/02). Nil means free-text. Deep-copied by Clone.
	OutputSchema map[string]any
	// InputSchema optionally validates task input at admission.
	InputSchema map[string]any
}

// Clone returns a deep copy safe for concurrent use.
func (a ResolvedAgent) Clone() ResolvedAgent {
	out := a
	out.Trace = a.Trace.clone()
	out.EffectiveTools = slices.Clone(a.EffectiveTools)
	out.DisallowedTools = slices.Clone(a.DisallowedTools)
	out.DisabledTools = slices.Clone(a.DisabledTools)
	if a.Skills != nil {
		s := slices.Clone(*a.Skills)
		out.Skills = &s
	}
	if a.SkillOrigins != nil {
		out.SkillOrigins = make(map[string]string, len(a.SkillOrigins))
		for k, v := range a.SkillOrigins {
			out.SkillOrigins[k] = v
		}
	}
	if a.MaxTurns != nil {
		v := *a.MaxTurns
		out.MaxTurns = &v
	}
	if a.TimeoutSeconds != nil {
		v := *a.TimeoutSeconds
		out.TimeoutSeconds = &v
	}
	if a.MaxTokens != nil {
		v := *a.MaxTokens
		out.MaxTokens = &v
	}
	out.OutputSchema = cloneAnyMap(a.OutputSchema)
	out.InputSchema = cloneAnyMap(a.InputSchema)
	return out
}

func cloneAnyMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return nil
	}
	// Re-parsing a map's own marshal output cannot fail; a hypothetical failure
	// leaves out nil, which is the same answer the check would have returned.
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

// DefinitionDigest returns the stable identity of an effective immutable
// definition. Routing persists this digest so resume cannot silently change
// the agent that owns work.
func (a ResolvedAgent) DefinitionDigest() (string, error) {
	// Field order is part of the wire format: encoding/json marshals in
	// declaration order. Provider is appended LAST and tagged omitempty so an
	// agent that declares no provider produces the exact byte sequence it did
	// before provider binding existed. Routing snapshots persisted in the
	// ledger carry these digests and resume re-validates them
	// (internal/coordinator/recovery.go), so reordering or untagging this
	// field silently invalidates every in-flight run. Pinned by
	// TestDefinitionDigestUnchangedWithoutProvider.
	type definition struct {
		Name, Description, Model, SystemPrompt, ParentName string
		MaxTurns                                           *int
		EffectiveTools, DisallowedTools                    []string
		Skills                                             *[]string
		SkillOrigins                                       map[string]string
		Source, Path                                       string
		Provider                                           string         `json:",omitempty"`
		TimeoutSeconds                                     *int           `json:",omitempty"`
		MaxTokens                                          *int           `json:",omitempty"`
		OutputSchema                                       map[string]any `json:",omitempty"`
		InputSchema                                        map[string]any `json:",omitempty"`
	}
	payload, err := json.Marshal(definition{
		Name: a.Name, Description: a.Description, Model: a.Model,
		SystemPrompt: a.SystemPrompt, ParentName: a.ParentName,
		MaxTurns: a.MaxTurns, EffectiveTools: a.EffectiveTools,
		DisallowedTools: a.DisallowedTools, Skills: a.Skills,
		SkillOrigins: a.SkillOrigins, Source: string(a.Provenance.Source), Path: a.Provenance.Path,
		Provider: a.Provider, TimeoutSeconds: a.TimeoutSeconds, MaxTokens: a.MaxTokens,
		OutputSchema: a.OutputSchema, InputSchema: a.InputSchema,
	})
	if err != nil {
		return "", fmt.Errorf("marshal agent definition %q: %w", a.Name, err)
	}
	digest := sha256.Sum256(payload)
	return fmt.Sprintf("sha256:%x", digest[:]), nil
}

// AgentRegistry is an immutable catalogue of resolved agents keyed by name.
type AgentRegistry struct {
	mu    sync.RWMutex
	by    map[string]ResolvedAgent
	order []string
}

// NewRegistry builds an empty agent registry.
func NewRegistry() *AgentRegistry {
	return &AgentRegistry{by: make(map[string]ResolvedAgent)}
}

// Publish inserts a resolved agent. Duplicate names are rejected.
// The stored value is a clone; callers cannot mutate the registry via the input.
func (r *AgentRegistry) Publish(agent ResolvedAgent) error {
	if r == nil {
		return fmt.Errorf("nil agent registry")
	}
	name := agent.Name
	if name == "" {
		return fmt.Errorf("agent name is empty")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.by[name]; exists {
		return fmt.Errorf("duplicate agent %q", name)
	}
	r.by[name] = agent.Clone()
	r.order = append(r.order, name)
	return nil
}

// Get returns a clone of the named agent.
func (r *AgentRegistry) Get(name string) (ResolvedAgent, bool) {
	if r == nil {
		return ResolvedAgent{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.by[name]
	if !ok {
		return ResolvedAgent{}, false
	}
	return a.Clone(), true
}

// List returns clones in publication order.
func (r *AgentRegistry) List() []ResolvedAgent {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ResolvedAgent, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, r.by[name].Clone())
	}
	return out
}

// Names returns sorted agent names.
func (r *AgentRegistry) Names() []string {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := slices.Clone(r.order)
	slices.Sort(out)
	return out
}

// Len returns the number of published agents.
func (r *AgentRegistry) Len() int {
	if r == nil {
		return 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.by)
}
