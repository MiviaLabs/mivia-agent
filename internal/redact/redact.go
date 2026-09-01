// Package redact applies the workspace's redaction policy to operator-visible
// text and structured values.
//
// Nothing is compiled in. What counts as a secret is a property of a workspace,
// not of this binary: four separate hardcoded pattern lists previously drifted
// apart and were wrong in both directions, over-redacting ordinary prose and
// missing credentials none of them happened to name. The policy comes from
// [privacy] in mivia.toml, recommended values ship in mivia.toml.example, and a
// workspace that configures nothing redacts nothing.
//
// That fails open by design. See .agents/rules/10-security-privacy.md.
package redact

import (
	"fmt"
	"regexp"
	"strings"
	"sync/atomic"
)

// DefaultPlaceholder is substituted for a match when the policy names none.
const DefaultPlaceholder = "[redacted]"

// maxDepth bounds recursion into crafted or deeply nested structures.
const maxDepth = 64

// Policy is a compiled redaction policy. The zero value and a nil *Policy
// redact nothing, so a path that runs before SetPolicy - tests, `mivia
// version`, any tool constructed directly - is unredacted rather than falling
// back to a compiled list.
type Policy struct {
	patterns    []*regexp.Regexp
	keyNames    []string
	placeholder string
}

// Compile builds a policy from configuration.
//
// An invalid pattern is an error naming the offending expression, never a
// silently dropped rule: a policy that quietly omits half its patterns is worse
// than one that refuses to start, because the operator believes they are
// covered.
func Compile(patterns, keyNames []string, placeholder string) (*Policy, error) {
	p := &Policy{placeholder: strings.TrimSpace(placeholder)}
	if p.placeholder == "" {
		p.placeholder = DefaultPlaceholder
	}
	for _, expr := range patterns {
		if strings.TrimSpace(expr) == "" {
			continue
		}
		compiled, err := regexp.Compile(expr)
		if err != nil {
			return nil, fmt.Errorf("redaction pattern %q: %w", expr, err)
		}
		p.patterns = append(p.patterns, compiled)
	}
	for _, name := range keyNames {
		if name = strings.TrimSpace(strings.ToLower(name)); name != "" {
			p.keyNames = append(p.keyNames, name)
		}
	}
	return p, nil
}

// empty reports whether the policy would change anything.
func (p *Policy) empty() bool {
	return p == nil || (len(p.patterns) == 0 && len(p.keyNames) == 0)
}

// Text replaces every pattern match with the placeholder.
//
// The placeholder is substituted verbatim (ReplaceAllLiteralString), never
// through regexp template expansion: the placeholder is operator-chosen
// text, and Go's Expand semantics would silently corrupt it - $0 re-emits
// the matched secret itself (the redaction echoing the exact text it exists
// to hide), while $1/${name} expand to empty or absent groups. JSONValue's
// key-elision path inserts the same placeholder literally, so both paths
// must agree on a literal placeholder.
func (p *Policy) Text(s string) string {
	if p == nil || len(p.patterns) == 0 || s == "" {
		return s
	}
	for _, re := range p.patterns {
		s = re.ReplaceAllLiteralString(s, p.placeholder)
	}
	return s
}

// JSONValue walks a decoded JSON value, replacing any value whose key matches
// the policy's key names and applying Text to the remaining string leaves.
// Strings are immutable, so leaves are replaced by their parent and the
// possibly-new value is returned.
func (p *Policy) JSONValue(v any) any { return p.jsonValue(v, 0) }

func (p *Policy) jsonValue(v any, depth int) any {
	if p.empty() || depth > maxDepth {
		return v
	}
	switch current := v.(type) {
	case map[string]any:
		for key, nested := range current {
			if p.matchesKey(key) {
				current[key] = p.placeholder
				continue
			}
			current[key] = p.jsonValue(nested, depth+1)
		}
		return current
	case []any:
		for i, nested := range current {
			current[i] = p.jsonValue(nested, depth+1)
		}
		return current
	case string:
		return p.Text(current)
	}
	return v
}

// matchesKey reports whether a JSON key names a value to elide wholesale.
func (p *Policy) matchesKey(key string) bool {
	lower := strings.ToLower(key)
	for _, name := range p.keyNames {
		if strings.Contains(lower, name) {
			return true
		}
	}
	return false
}

// active is the process-wide policy. Redaction sites are spread across packages
// that have no access to resolved config (the runtime dispatcher, the TUI tool
// panel), and threading a policy through those signatures would touch the whole
// dispatch and render path. This mirrors tools.SetRedactToolArgs.
var active atomic.Pointer[Policy]

// SetPolicy installs the process-wide policy. Call once, after config load.
func SetPolicy(p *Policy) { active.Store(p) }

// Current returns the installed policy, or nil when none is set.
func Current() *Policy { return active.Load() }

// Active reports whether the process-wide policy would redact anything.
//
// A caller needs this to decide SHAPE, not content. Redaction is a regex over
// one string, so it cannot match a secret split across two strings: a policy
// that would catch a key in a whole message catches nothing when the same key
// arrives as three fragments. A producer that can choose between sending
// fragments and sending one message must therefore know whether a policy is
// active before it chooses.
func Active() bool { return !active.Load().empty() }

// Text applies the process-wide policy.
func Text(s string) string { return active.Load().Text(s) }

// JSONValue applies the process-wide policy.
func JSONValue(v any) any { return active.Load().JSONValue(v) }
