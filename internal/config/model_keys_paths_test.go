package config

import (
	"strings"
	"testing"
)

func auditDoc(t *testing.T, body string) error {
	t.Helper()
	return auditModelKeys([]byte(body))
}

// The walk reads keys wherever TOML lets an operator write them. These are the
// spellings a reflection view used to cover and a naive header-only walk would
// silently drop - a catalog written inline must be audited exactly as one
// written in headers, or the closed shape holds for some syntax only.
func TestModelKeyAuditReadsEveryProviderSpelling(t *testing.T) {
	cases := map[string]string{
		"inline providers table": `providers = { zai = { models = [{ name = "m", typo = 1 }] } }`,
		"inline provider value":  `providers.zai = { models = [{ name = "m", typo = 1 }] }`,
		"inline models array":    "[providers.zai]\nmodels = [{ name = \"m\", typo = 1 }]",
		"array of tables":        "[[providers.zai.models]]\nname = \"m\"\ntypo = 1",
	}
	for label, body := range cases {
		t.Run(label, func(t *testing.T) {
			err := auditDoc(t, body)
			if err == nil {
				t.Fatalf("%s: an unknown key must be named", label)
			}
			if !strings.Contains(err.Error(), `"typo"`) {
				t.Fatalf("%s: error = %v, want it to name typo", label, err)
			}
		})
	}
}

// The index in the message is the operator's way of finding the entry, so it
// must count entries the same way regardless of spelling.
func TestModelKeyAuditReportsTheOffendingEntryIndex(t *testing.T) {
	cases := map[string]string{
		"inline": `[providers.zai]
models = [{ name = "a" }, { name = "b" }, { name = "c", typo = 1 }]`,
		"headers": `[[providers.zai.models]]
name = "a"

[[providers.zai.models]]
name = "b"

[[providers.zai.models]]
name = "c"
typo = 1`,
	}
	for label, body := range cases {
		t.Run(label, func(t *testing.T) {
			err := auditDoc(t, body)
			if err == nil || !strings.Contains(err.Error(), "models[2]") {
				t.Fatalf("%s: error = %v, want it to name models[2]", label, err)
			}
		})
	}
}

// Shapes the audit must pass through untouched: they carry no model keys, and
// rejecting them here would duplicate - and eventually contradict - the strict
// decode that owns value and shape errors.
func TestModelKeyAuditIgnoresWhatIsNotAModelKey(t *testing.T) {
	cases := map[string]string{
		"no providers at all":     "[chat]\nmax_tokens = 8192",
		"provider without models": "[providers.zai]\nbase_url = \"https://example.test\"",
		"models is not an array":  "[providers.zai]\nmodels = 5",
		"models entries are not tables": `[providers.zai]
models = [1, 2]`,
		"a sibling section with its own keys": `[[hooks]]
event = "PreToolUse"

[[providers.zai.models]]
name = "m"`,
		"deeply nested unrelated table": "[providers.zai.limits.daily]\ntokens = 5",
		"an inline table under a known key": `[providers.zai]
models = [{ name = "m", context_window_tokens = 1 }]`,
	}
	for label, body := range cases {
		t.Run(label, func(t *testing.T) {
			if err := auditDoc(t, body); err != nil {
				t.Fatalf("%s: audit rejected a document carrying no unknown model key: %v", label, err)
			}
		})
	}
}

// A key written with no parts cannot name a field. The walk skips it rather
// than indexing into an empty slice, and the strict decode owns the rejection.
func TestModelKeyAuditSurvivesDegenerateKeys(t *testing.T) {
	if err := auditDoc(t, "[providers]\n"); err != nil {
		t.Fatalf("a bare providers table must pass: %v", err)
	}
	if err := auditDoc(t, "[providers.zai.models]\nname = \"m\"\ntypo = 1"); err == nil {
		t.Fatal("a models table written as a plain table must still be audited")
	}
}
