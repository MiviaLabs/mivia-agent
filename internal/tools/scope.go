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
		if scopeAdmits(t, opts.Mode, denied, opts.Allowlist) {
			out.Register(t)
		}
	}
	return out
}

// ScopedRegistryWithTail is ScopedRegistry with an explicit ordering contract
// for host-mediated tool admission (plan tools/05 D8).
//
// opts.Allowlist selects the core block, which is materialized in src order.
// Each name in tail is then appended in tail order, subject to the identical
// scope rules with its own name as the allowlist for that decision, plus one
// admission-only restriction: a denied name (compiled denylist or operator
// ExtraDenylist) is never admitted, in either mode. The extra restriction is
// what makes the guarantee hold on its own. Without it a ScopeRoot tail would
// re-enter an operator guardrail denial (INV-AG-29) that the caller's real
// allowlist excludes, because at root ScopedRegistry keeps a denylisted name
// whenever the allowlist carries it - and the per-name allowlist used here
// always carries it. Admission is a publication decision, never a grant: it can
// only ever narrow what the caller's own scope already authorized.
//
// Because admitted tools land after the core block instead of
// materializing inside it, the core block's serialized schemas are
// byte-identical across admissions and the privileged session tools a
// dispatcher registers afterwards stay at the end.
//
// Names absent from src, already in the core block, or repeated in tail are
// skipped; Register is idempotent by name regardless.
func ScopedRegistryWithTail(src *Registry, opts ScopeOptions, tail []string) *Registry {
	out := ScopedRegistry(src, opts)
	if src == nil {
		return out
	}
	denied := MandatoryDenylistSet(opts.ExtraDenylist...)
	for _, name := range tail {
		if _, already := out.Get(name); already {
			continue
		}
		if denied[name] {
			// A denial outranks admission in both modes. At root a privileged
			// tool sharing this name is already in the core block above, so
			// this only ever rejects a non-privileged denied name.
			continue
		}
		t, ok := src.Get(name)
		if !ok {
			continue
		}
		// The per-name allowlist can only ever match, so this call decides on
		// mode and the privileged marker alone; it is written this way so the
		// tail keeps sharing one filter with ScopedRegistry rather than
		// re-deriving the rules.
		if scopeAdmits(t, opts.Mode, denied, map[string]struct{}{name: {}}) {
			out.Register(t)
		}
	}
	return out
}

// scopeAdmits is the single filter decision shared by ScopedRegistry and
// ScopedRegistryWithTail. A nil allowlist means "no allowlist filter".
func scopeAdmits(t Tool, mode ScopeMode, denied map[string]bool, allowlist map[string]struct{}) bool {
	name := t.Name()
	_, privileged := t.(PrivilegedTool)
	if mode == ScopeRoot {
		if privileged {
			// Root retains privileged/delegation tools unconditionally.
			return true
		}
		if denied[name] {
			// Non-privileged tools that share a denylist name are kept at
			// root only when no allowlist is set, or when the name is
			// allowlisted. An operator guardrail denial (ExtraDenylist)
			// must not be re-admitted past the agent allowlist
			// (INV-AG-29 execution denial); the agent's effective set
			// already excludes these names at resolve time.
			if allowlist == nil {
				return true
			}
			_, ok := allowlist[name]
			return ok
		}
		if allowlist != nil {
			_, ok := allowlist[name]
			return ok
		}
		return true
	}
	// ScopeSpawned
	if privileged || denied[name] {
		return false
	}
	if allowlist != nil {
		_, ok := allowlist[name]
		return ok
	}
	return true
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
