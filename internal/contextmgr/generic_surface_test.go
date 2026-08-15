package contextmgr

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// contextmgr owns the compaction trigger, target, pruning, and calibration
// math. It must reach every provider-specific context-billing distinction
// (e.g. how much of a reasoning-replay provider's ReasoningContent history is
// actually billed) through provider.ContextAccountingProfile, opaquely -
// never by branching on a provider's name. A provider name appearing here
// would mean some accounting decision bypassed the profile and hard-coded
// one provider's behavior into policy every other provider also runs
// through.
//
// This mirrors internal/tools/generic_surface_test.go's rule-60 grep-gate,
// scoped to this package's own source instead of model-facing tool text.

// providerNamePatterns are the builtin provider identifiers that must never
// appear in contextmgr's own source. Keep this list in sync with
// internal/providerregistry's builtin set.
var providerNamePatterns = []struct {
	name string
	re   *regexp.Regexp
}{
	{"deepseek", regexp.MustCompile(`(?i)\bdeepseek\b`)},
	{"openrouter", regexp.MustCompile(`(?i)\bopenrouter\b`)},
	{"zai/z.ai", regexp.MustCompile(`(?i)\bz\.?ai\b`)},
	{"ollama", regexp.MustCompile(`(?i)\bollama\b`)},
}

// TestPackageSourceIsProviderAgnostic greps every non-test .go file in this
// package for a builtin provider's name. Test files are exempt: fixtures
// exercising a specific provider's documented behavior (e.g. pinning the
// terminal-exchange profile against a scenario named after the provider that
// motivated it) legitimately name it, same as internal/provider's own
// per-provider constructor tests do; the package's real accounting logic
// must not.
func TestPackageSourceIsProviderAgnostic(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var failures []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		for _, p := range providerNamePatterns {
			if p.re.MatchString(text) {
				failures = append(failures, name+": matches provider name pattern "+p.name)
			}
		}
	}
	if len(failures) > 0 {
		t.Fatalf("contextmgr must reach provider distinctions only through provider.ContextAccountingProfile, never a provider's name:\n  %s",
			strings.Join(failures, "\n  "))
	}
}
