package tools

import "strings"

// ScopeMode selects root vs spawned registry filtering policy.
type ScopeMode int

const (
	// ScopeSpawned applies the mandatory denylist and drops PrivilegedTool
	// markers. Nested multi-step instances use this mode.
	ScopeSpawned ScopeMode = iota
	// ScopeRoot keeps privileged/delegation tools needed for the root session
	// to dispatch work, even when those names appear in the denylist.
	// Allowlist intersection still applies to non-privileged tools when set.
	ScopeRoot
)

// CompiledMandatoryDenylist is the baseline tool-name denylist for spawned
// agents. Config may only ADD names via [agents.guardrails]
// mandatory_tool_denylist; it may never remove these.
var CompiledMandatoryDenylist = []string{
	"delegate",
	"dispatch_tasks",
	"spawn_agent",
	"inspect_agents",
	"join_run",
	"cancel_run",
}

// ScopeOptions configures ScopedRegistry.
type ScopeOptions struct {
	// Mode selects root vs spawned policy.
	Mode ScopeMode
	// Allowlist, when non-nil, is the set of tool names the agent may use.
	// Nil means "no allowlist filter" (all non-denied tools pass). An empty
	// non-nil map means deny-all (after denylist/privileged rules).
	Allowlist map[string]struct{}
	// ExtraDenylist adds operator denylist names on top of the compiled set.
	// Only applied in ScopeSpawned (and always as name denials for non-privileged
	// tools in ScopeRoot when listed in Allowlist intersection paths).
	ExtraDenylist []string
}

// MandatoryDenylistSet returns the compiled denylist plus optional additions.
func MandatoryDenylistSet(extra ...string) map[string]bool {
	out := make(map[string]bool, len(CompiledMandatoryDenylist)+len(extra))
	for _, n := range CompiledMandatoryDenylist {
		out[n] = true
	}
	for _, n := range extra {
		n = strings.TrimSpace(n)
		if n != "" {
			out[n] = true
		}
	}
	return out
}

// ScopedRegistry returns a fresh registry derived from src without mutating it.
// Tool object identity (including PrivilegedTool markers) is preserved for
// tools that pass the filter - filtering is not name-only reconstruction.
//
// ScopeSpawned: drop mandatory denylist names and any PrivilegedTool.
// ScopeRoot: keep PrivilegedTool and denylist names (delegation stays available);
// when Allowlist is non-nil, non-privileged tools must be in the allowlist.
func ScopedRegistry(src *Registry, opts ScopeOptions) *Registry {
	out := NewRegistry()
	if src == nil {
		return out
	}
	denied := MandatoryDenylistSet(opts.ExtraDenylist...)
	for _, t := range src.List() {
		name := t.Name()
		_, privileged := t.(PrivilegedTool)
		switch opts.Mode {
		case ScopeRoot:
			if privileged {
				// Root retains privileged/delegation tools unconditionally.
				out.Register(t)
				continue
			}
			if denied[name] {
				// Non-privileged tools that share a denylist name are kept at
				// root only when no allowlist is set, or when the name is
				// allowlisted. An operator guardrail denial (ExtraDenylist)
				// must not be re-admitted past the agent allowlist
				// (INV-AG-29 execution denial); the agent's effective set
				// already excludes these names at resolve time.
				if opts.Allowlist == nil {
					out.Register(t)
					continue
				}
				if _, ok := opts.Allowlist[name]; ok {
					out.Register(t)
				}
				continue
			}
			if opts.Allowlist != nil {
				if _, ok := opts.Allowlist[name]; !ok {
					continue
				}
			}
			out.Register(t)
		default: // ScopeSpawned
			if privileged {
				continue
			}
			if denied[name] {
				continue
			}
			if opts.Allowlist != nil {
				if _, ok := opts.Allowlist[name]; !ok {
					continue
				}
			}
			out.Register(t)
		}
	}
	return out
}

// FilterNames applies denylist + optional allowlist to a name set without a
// registry. Used by agent resolution policy before a registry exists.
func FilterNames(names []string, mode ScopeMode, allowlist map[string]struct{}, extraDenylist []string) []string {
	denied := MandatoryDenylistSet(extraDenylist...)
	var out []string
	for _, name := range names {
		if mode == ScopeSpawned && denied[name] {
			continue
		}
		if allowlist != nil {
			if _, ok := allowlist[name]; !ok {
				continue
			}
		}
		out = append(out, name)
	}
	return out
}
