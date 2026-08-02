package reasoning

import (
	"strings"
	"testing"
)

func TestParseLevelAcceptsEveryNamedLevel(t *testing.T) {
	for _, want := range []Level{Off, Minimal, Low, Medium, High, XHigh, Max} {
		got, err := ParseLevel(string(want))
		if err != nil {
			t.Fatalf("ParseLevel(%q): unexpected error: %v", want, err)
		}
		if got != want {
			t.Fatalf("ParseLevel(%q) = %q, want %q", want, got, want)
		}
	}
}

func TestParseLevelTreatsEmptyAsUnset(t *testing.T) {
	got, err := ParseLevel("")
	if err != nil {
		t.Fatalf("ParseLevel(\"\"): unexpected error: %v", err)
	}
	if got != "" {
		t.Fatalf("ParseLevel(\"\") = %q, want unset", got)
	}
	if got.Active() {
		t.Fatal("the unset level must not be active")
	}
}

func TestParseLevelRejectsUnknownValue(t *testing.T) {
	if _, err := ParseLevel("turbo"); err == nil {
		t.Fatal("ParseLevel(\"turbo\") must fail")
	} else if !strings.Contains(err.Error(), "turbo") {
		t.Fatalf("error must name the rejected value, got %v", err)
	}
}

// A level is spelled exactly as configured. Accepting "HIGH" or " high " would
// make the config surface silently forgiving in one place and strict in every
// other closed TOML object.
func TestParseLevelIsCaseAndSpaceSensitive(t *testing.T) {
	for _, bad := range []string{"HIGH", " high", "high "} {
		if _, err := ParseLevel(bad); err == nil {
			t.Fatalf("ParseLevel(%q) must fail", bad)
		}
	}
}

func TestActiveIsTrueForEveryNamedLevel(t *testing.T) {
	for _, level := range []Level{Off, Minimal, Low, Medium, High, XHigh, Max} {
		if !level.Active() {
			t.Fatalf("%q must be active; only the empty level is unset", level)
		}
	}
}

func TestParseDialectAcceptsEveryNamedDialect(t *testing.T) {
	for _, want := range []Dialect{DialectOpenAI, DialectOpenRouter, DialectThinking, DialectThinkingEffort, DialectNone} {
		got, err := ParseDialect(string(want))
		if err != nil {
			t.Fatalf("ParseDialect(%q): unexpected error: %v", want, err)
		}
		if got != want {
			t.Fatalf("ParseDialect(%q) = %q, want %q", want, got, want)
		}
	}
}

func TestParseDialectTreatsEmptyAsUnset(t *testing.T) {
	got, err := ParseDialect("")
	if err != nil {
		t.Fatalf("ParseDialect(\"\"): unexpected error: %v", err)
	}
	if got != "" {
		t.Fatalf("ParseDialect(\"\") = %q, want unset", got)
	}
}

func TestParseDialectRejectsUnknownValue(t *testing.T) {
	if _, err := ParseDialect("qwen"); err == nil {
		t.Fatal("ParseDialect(\"qwen\") must fail")
	} else if !strings.Contains(err.Error(), "qwen") {
		t.Fatalf("error must name the rejected value, got %v", err)
	}
}

func TestDefaultDialectCoversVettedProvidersOnly(t *testing.T) {
	cases := []struct {
		provider string
		want     Dialect
		ok       bool
	}{
		{"zai", DialectThinking, true},
		{"openrouter", DialectOpenAI, true},
		// DeepSeek thinking mode needs reasoning_content replay on tool-call
		// turns, which this slice does not implement. No default dialect keeps
		// it out of the initial rollout; opting in must be explicit.
		{"deepseek", "", false},
		{"kimi", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		got, ok := DefaultDialect(tc.provider)
		if ok != tc.ok {
			t.Fatalf("DefaultDialect(%q) ok = %v, want %v", tc.provider, ok, tc.ok)
		}
		if got != tc.want {
			t.Fatalf("DefaultDialect(%q) = %q, want %q", tc.provider, got, tc.want)
		}
	}
}

// The provider name reaching DefaultDialect comes from config, which lowercases
// provider keys. Matching is exact so an unexpected spelling fails closed
// (nothing sent) rather than guessing a wire shape.
func TestDefaultDialectDoesNotGuessOnUnexpectedSpelling(t *testing.T) {
	if _, ok := DefaultDialect("ZAI"); ok {
		t.Fatal("DefaultDialect must not match a differently-cased provider name")
	}
}

func TestSettingIsActiveOnlyWithALevel(t *testing.T) {
	if (Setting{}).Active() {
		t.Fatal("the zero Setting must not be active")
	}
	// A dialect on its own declares capability for a model dialled off.
	if (Setting{Dialect: DialectThinking}).Active() {
		t.Fatal("a dialect without a level must not be active")
	}
	if !(Setting{Level: High, Dialect: DialectThinking}).Active() {
		t.Fatal("a level makes the setting active")
	}
	if !(Setting{Level: Off}).Active() {
		t.Fatal("off is an explicit instruction to disable, not an unset dial")
	}
}
