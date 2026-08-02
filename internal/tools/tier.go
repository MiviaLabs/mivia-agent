package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
)

// LoadToolsToolName is the discovery tool that stages deferred-tool admission.
// It is a privileged session tool: the host registers it on the root registry
// only when the selected agent actually defers something.
const LoadToolsToolName = "load_tools"

// MaxAdmissionPublications bounds how many surface widenings one agent binding
// may cause. Idempotent and failing calls never consume it.
const MaxAdmissionPublications = 8

// MaxAdmissionAttempts bounds total load_tools calls per agent binding,
// including idempotent and failing ones, so a looping model is stopped even
// when it never widens anything.
const MaxAdmissionAttempts = 32

// TierCandidate is one deferred tool's advertisement metadata. The host builds
// the candidate list once per agent binding, in live-registry order, so both
// the frozen prompt index and query matching see the same order.
type TierCandidate struct {
	Name        string
	Description string
}

// Tiers is an agent binding's split of its effective tool set into the
// always-advertised core and the deferred remainder.
type Tiers struct {
	Core     []string
	Deferred []string
}

// SplitTiers splits effective into core and deferred tools.
//
// A nil core list means "no core configured", which keeps every effective tool
// core and leaves Deferred empty - the zero-config path is fully inert. A
// non-nil core list is intersected with effective: naming a tool the agent
// cannot invoke never widens authority. Both output lists preserve the order
// of effective, which is the live registry's registration order.
func SplitTiers(effective []string, core []string) Tiers {
	if core == nil {
		return Tiers{Core: slices.Clone(effective)}
	}
	coreSet := make(map[string]struct{}, len(core))
	for _, name := range core {
		name = strings.TrimSpace(name)
		if name != "" {
			coreSet[name] = struct{}{}
		}
	}
	out := Tiers{}
	for _, name := range effective {
		if _, ok := coreSet[name]; ok {
			out.Core = append(out.Core, name)
			continue
		}
		out.Deferred = append(out.Deferred, name)
	}
	return out
}

// MatchDeferred returns candidate names whose name or description contains
// query, compared with strings.ToLower and no locale collation, in candidate
// (registration) order. An empty query matches nothing.
func MatchDeferred(query string, candidates []TierCandidate) []string {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return nil
	}
	var out []string
	for _, candidate := range candidates {
		haystack := strings.ToLower(candidate.Name + " " + candidate.Description)
		if strings.Contains(haystack, query) {
			out = append(out, candidate.Name)
		}
	}
	return out
}

// DeferredIndex renders the frozen deferred-tool index injected into the system
// prompt once per agent binding. It is generated from the binding's full
// deferred set and never re-rendered after an admission, so system-prompt bytes
// stay stable for the binding's lifetime.
func DeferredIndex(candidates []TierCandidate) string {
	if len(candidates) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Additional tools are available but not currently loaded. ")
	b.WriteString("Call ")
	b.WriteString(LoadToolsToolName)
	b.WriteString(" to load the ones you need; they become callable on your next turn.\n")
	for _, candidate := range candidates {
		b.WriteString("- ")
		b.WriteString(candidate.Name)
		if line := firstLine(candidate.Description); line != "" {
			b.WriteString(": ")
			b.WriteString(line)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// firstLine reduces a tool description to a single-sentence one-liner so the
// frozen index stays small next to the schemas it replaces.
func firstLine(description string) string {
	description = strings.TrimSpace(description)
	if description == "" {
		return ""
	}
	if idx := strings.IndexByte(description, '\n'); idx >= 0 {
		description = description[:idx]
	}
	if idx := strings.IndexByte(description, '.'); idx >= 0 {
		description = description[:idx]
	}
	return strings.TrimSpace(description)
}

// AdmissionDigest fingerprints the tier decision an admitted set was made
// against: the agent name plus its core and deferred tiers. A resumed session
// whose digest no longer matches drops its admitted set fail-closed, because
// the names in it may no longer mean what they meant when they were admitted.
func AdmissionDigest(agentName string, tiers Tiers) string {
	sum := sha256.New()
	sum.Write([]byte(agentName))
	sum.Write([]byte{0})
	for _, name := range tiers.Core {
		sum.Write([]byte(name))
		sum.Write([]byte{0})
	}
	sum.Write([]byte{1})
	for _, name := range tiers.Deferred {
		sum.Write([]byte(name))
		sum.Write([]byte{0})
	}
	return hex.EncodeToString(sum.Sum(nil))
}
