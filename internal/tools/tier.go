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

// MaxAdmissionNamesPerCall bounds the "names" array a single load_tools call
// may stage. It is the per-call cap on that array (single source of truth; the
// cli schema's maxItems references it). Together with MaxAdmissionPublications
// it bounds the admitted set: at most 8 widenings of 64 names, 512 names, may
// ever be admitted into one binding's surface, and StageToolAdmission enforces
// that total so a perpetually-deferred stage cannot exceed it either.
const MaxAdmissionNamesPerCall = 64

// MaxAdvertisedTools bounds the per-request tools[] array to the tightest
// documented ceiling among supported OpenAI-compatible providers (DeepSeek's
// chat-completions API caps "tools" at 128 functions). The advertised union
// (plan tools-advertising/01: core tier plus every deferred candidate, pinned
// for the binding's lifetime so the wire tools[] array never changes mid-turn)
// is truncated to this many names, core-then-deferred, when it would exceed
// the cap. Truncated names stay authorized and executable once admitted; they
// are simply never advertised for this binding.
const MaxAdvertisedTools = 128

// AdvertisedNames returns core then deferred tool names for the advertised
// union, truncated to MaxAdvertisedTools minus reserve. reserve budgets slots
// for schemas the caller will append on top of these names (e.g. load_tools),
// so the FINAL wire tools[] array - names plus whatever the caller adds -
// never exceeds MaxAdvertisedTools; passing 0 truncates to MaxAdvertisedTools
// directly. It reports how many names were dropped by truncation so callers
// can surface the loss instead of it going silent (no silent caps).
func AdvertisedNames(core, deferred []string, reserve int) (names []string, dropped int) {
	budget := MaxAdvertisedTools - reserve
	if budget < 0 {
		budget = 0
	}
	names = make([]string, 0, len(core)+len(deferred))
	names = append(names, core...)
	names = append(names, deferred...)
	if len(names) <= budget {
		return names, 0
	}
	dropped = len(names) - budget
	return names[:budget], dropped
}

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
	b.WriteString("Additional tools below are authorized and their schemas are visible to you, but they are locked. ")
	b.WriteString("Call ")
	b.WriteString(LoadToolsToolName)
	b.WriteString(" to load the ones you need. ")
	b.WriteString("Calling a locked tool directly is not refused: the call runs immediately, and the tool is loaded for later steps too.\n")
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

// FirstLine is the exported form of firstLine, for callers outside this
// package that need the identical one-line summary DeferredIndex uses (e.g.
// shrinking a deferred tool's advertised wire schema without duplicating the
// sentence-boundary logic).
func FirstLine(description string) string { return firstLine(description) }

// firstLine reduces a tool description to a single-sentence one-liner so the
// frozen index stays small next to the schemas it replaces.
func firstLine(description string) string {
	description = strings.TrimSpace(description)
	if description == "" {
		return ""
	}
	if idx := strings.IndexByte(description, '\n'); idx >= 0 {
		description = strings.TrimSpace(description[:idx])
	}
	if idx := sentenceEnd(description); idx > 0 {
		return strings.TrimSpace(description[:idx])
	}
	return description
}

// sentenceEnd returns the index of the first period that actually terminates a
// sentence, or -1 when there is none. A period only counts when it is followed
// by whitespace or the end of the text and sits outside any open quote or
// bracket, so a description like `... (default ".")` keeps its delimiters
// balanced instead of being cut mid-token. A period with nothing before it is
// not a terminator either: cutting there would leave the tool with no
// description at all.
func sentenceEnd(description string) int {
	quoted := false
	depth := 0
	for i := 0; i < len(description); i++ {
		switch description[i] {
		case '"':
			quoted = !quoted
		case '(', '[':
			depth++
		case ')', ']':
			if depth > 0 {
				depth--
			}
		case '.':
			if quoted || depth > 0 {
				continue
			}
			if i+1 < len(description) && !isSpaceByte(description[i+1]) {
				continue
			}
			if strings.TrimSpace(description[:i]) != "" {
				return i
			}
		}
	}
	return -1
}

func isSpaceByte(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
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
