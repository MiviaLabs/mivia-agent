package agents

import (
	"slices"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

const descriptionMaxLen = 200

// SanitizeDescription cleans agent description text for model-facing surfaces.
func SanitizeDescription(text string) string {
	s, _ := skills.SanitizeModelFacingText(text, descriptionMaxLen)
	return s
}

// applyToolPolicy implements evaluation order:
// mandatory denylist → disallowed_tools → allowlist.
// A name on the mandatory denylist is denied even if it appears in the allowlist.
func applyToolPolicy(allow []string, disallowed []string, opts ResolveOptions) ([]string, error) {
	denied := tools.MandatoryDenylistSet(opts.Global.MandatoryToolDenylistAdditions...)
	for _, n := range disallowed {
		n = strings.TrimSpace(n)
		if n != "" {
			denied[n] = true
		}
	}
	allowSet := make(map[string]struct{}, len(allow))
	var effective []string
	for _, n := range allow {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		if denied[n] {
			// Mandatory denylist wins over allowlist (M2).
			continue
		}
		if _, dup := allowSet[n]; dup {
			continue
		}
		allowSet[n] = struct{}{}
		effective = append(effective, n)
	}
	return effective, nil
}

// AllowlistSet converts an effective tools list to a set for ScopedRegistry.
func AllowlistSet(names []string) map[string]struct{} {
	out := make(map[string]struct{}, len(names))
	for _, n := range names {
		out[n] = struct{}{}
	}
	return out
}

// IntersectWithRegistry drops tools that are known but absent from the live
// registry (disabled). Returns the filtered list and names dropped as disabled.
func IntersectWithRegistry(effective []string, reg *tools.Registry) (kept, disabled []string) {
	if reg == nil {
		return slices.Clone(effective), nil
	}
	for _, n := range effective {
		if _, ok := reg.Get(n); ok {
			kept = append(kept, n)
			continue
		}
		if tools.IsKnownToolName(n) {
			disabled = append(disabled, n)
			continue
		}
		kept = append(kept, n)
	}
	return kept, disabled
}

// EvalOrderReason documents why a tool was denied (for tests and diagnostics).
func EvalOrderReason(name string, allow []string, disallowed []string, extraDenylist []string) string {
	denied := tools.MandatoryDenylistSet(extraDenylist...)
	if denied[name] {
		return "mandatory denylist"
	}
	for _, d := range disallowed {
		if d == name {
			return "disallowed_tools"
		}
	}
	for _, a := range allow {
		if a == name {
			return "allowed"
		}
	}
	return "not in allowlist"
}

// TightenGuardrails merges workspace guardrails onto a user floor. Workspace
// may only tighten (false→true for booleans; denylist may only add).
func TightenGuardrails(user, workspace config.AgentsGlobal) config.AgentsGlobal {
	out := user
	if workspace.RequireExplicitTools {
		out.RequireExplicitTools = true
	}
	if workspace.FailOnEmptyToolset {
		out.FailOnEmptyToolset = true
	}
	seen := map[string]bool{}
	var merged []string
	for _, n := range user.MandatoryToolDenylistAdditions {
		if !seen[n] {
			seen[n] = true
			merged = append(merged, n)
		}
	}
	for _, n := range workspace.MandatoryToolDenylistAdditions {
		if !seen[n] {
			seen[n] = true
			merged = append(merged, n)
		}
	}
	out.MandatoryToolDenylistAdditions = merged
	return out
}
