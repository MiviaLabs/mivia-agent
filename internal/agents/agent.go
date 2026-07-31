// Package agents resolves file-backed agent definitions into immutable
// runtime snapshots. It does not construct CLI dispatchers or registries.
package agents

import (
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
	Name            string
	Description     string
	Model           string
	MaxTurns        *int // nil = unset
	SystemPrompt    string
	EffectiveTools  []string // final allowlist after inheritance/deltas/guardrails
	DisallowedTools []string // effective denylist names applied before allowlist
	Provenance      Provenance
	// ParentName is the resolved parent, empty when none.
	ParentName string
	// DisabledTools are catalogue-known tools dropped because they are absent
	// from the position's registry (filled by Layer C; empty after resolve).
	DisabledTools []string
}

// Clone returns a deep copy safe for concurrent use.
func (a ResolvedAgent) Clone() ResolvedAgent {
	out := a
	out.EffectiveTools = slices.Clone(a.EffectiveTools)
	out.DisallowedTools = slices.Clone(a.DisallowedTools)
	out.DisabledTools = slices.Clone(a.DisabledTools)
	if a.MaxTurns != nil {
		v := *a.MaxTurns
		out.MaxTurns = &v
	}
	return out
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
