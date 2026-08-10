// Package verifier provides a host-owned catalogue of deterministic verifier
// profiles for evidence_gate steps. Workflow files may name a registered
// profile only; they cannot supply shell or command strings. One exception:
// an evidence_gate step may declare a sandboxed command (a bare executable
// name plus argv, never a shell string) that runs inside the same isolation
// as the fixed profiles — a copied worktree without secrets, no network, no
// host home, and an empty environment.
package verifier

import (
	"context"
	"fmt"
	"sync"

	"github.com/MiviaLabs/mivia-agent/internal/secretpath"
)

// Check is one named host verification check result.
type Check struct {
	Name   string `json:"name"`
	Status string `json:"status"`          // passed | failed | skipped
	Class  string `json:"class,omitempty"` // source | host
	Detail string `json:"detail,omitempty"`
	// Failures is a bounded, language-agnostic list of failing items the gate
	// detected in its output (test names, compile errors, assertion
	// messages). It is complete even when Detail is truncated, so a repair
	// step always learns what must be fixed.
	Failures []string `json:"failures,omitempty"`
}

// Result is schema-shaped verification evidence (verification-v1).
type Result struct {
	Status string  `json:"status"` // passed | failed
	Checks []Check `json:"checks"`
}

// Repairable reports whether all failed checks come from the delivered source.
func (r Result) Repairable() bool {
	failed := false
	for _, check := range r.Checks {
		if check.Status != "failed" {
			continue
		}
		failed = true
		if check.Class == "host" {
			return false
		}
	}
	return failed
}

// Request is the fixed host context for one verifier invocation.
type Request struct {
	// WorkDir is the workspace directory for host checks. Empty means cwd.
	WorkDir string
	// StepID is the evidence_gate step identity (for diagnostics only).
	StepID string
	// RunID is the workflow run identity (for diagnostics only).
	RunID string
	// ModuleBaseline pins Go module inputs from workflow admission.
	ModuleBaseline *GoModuleBaseline
}

// GoModuleBaseline pins module files before a workflow agent can edit them.
type GoModuleBaseline struct {
	GoMod []byte
	GoSum []byte
}

// Profile is one registered host verifier implementation.
type Profile interface {
	Name() string
	Verify(ctx context.Context, req Request) (Result, error)
}

// Catalogue looks up host-owned verifier profiles by name.
type Catalogue struct {
	mu       sync.RWMutex
	profiles map[string]Profile
}

// NewCatalogue returns an empty catalogue.
func NewCatalogue() *Catalogue {
	return &Catalogue{profiles: make(map[string]Profile)}
}

// DefaultCatalogue returns a catalogue with fixed host-owned Go profiles.
func DefaultCatalogue(policy secretpath.Policy) *Catalogue {
	c := NewCatalogue()
	for _, profile := range defaultGoProfiles(policy) {
		c.profiles[profile.Name()] = profile
	}
	return c
}

// Register adds a profile. Duplicate names fail closed.
func (c *Catalogue) Register(p Profile) error {
	if c == nil {
		return fmt.Errorf("verifier catalogue is nil")
	}
	if p == nil {
		return fmt.Errorf("verifier profile is nil")
	}
	name := p.Name()
	if name == "" {
		return fmt.Errorf("verifier profile name is empty")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.profiles[name]; ok {
		return fmt.Errorf("verifier %q is already registered", name)
	}
	c.profiles[name] = p
	return nil
}

// Lookup returns a registered profile. Unknown names fail closed without
// dispatching any command.
func (c *Catalogue) Lookup(name string) (Profile, error) {
	if c == nil {
		return nil, fmt.Errorf("verifier catalogue is nil")
	}
	if name == "" {
		return nil, fmt.Errorf("verifier name is empty")
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	p, ok := c.profiles[name]
	if !ok {
		return nil, fmt.Errorf("verifier %q is not registered", name)
	}
	return p, nil
}

// Names returns registered profile names in unspecified order.
func (c *Catalogue) Names() []string {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]string, 0, len(c.profiles))
	for name := range c.profiles {
		out = append(out, name)
	}
	return out
}
