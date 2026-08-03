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
		{"deepseek", DialectThinkingEffort, true},
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

func TestFormatLevels(t *testing.T) {
	if got := FormatLevels(nil); got != "" {
		t.Fatalf("empty set = %q, want the empty string", got)
	}
	if got := FormatLevels([]Level{High}); got != "high" {
		t.Fatalf("single = %q", got)
	}
	// Order is preserved: the caller's order is the configured order, which is
	// what a picker and an error message both need to agree on.
	if got := FormatLevels([]Level{Max, Low, Medium}); got != "max, low, medium" {
		t.Fatalf("ordered set = %q", got)
	}
}

func TestFormatLevelsQuoted(t *testing.T) {
	if got := FormatLevelsQuoted(nil); got != "" {
		t.Fatalf("empty set = %q, want the empty string", got)
	}
	if got := FormatLevelsQuoted([]Level{Max, Low}); got != `"max", "low"` {
		t.Fatalf("ordered set = %q", got)
	}
}

// A config load error prints to stderr and may carry a level exactly as the
// operator typed it, before any validation has rejected it.
func TestFormatLevelsQuotedEscapesControlBytes(t *testing.T) {
	got := FormatLevelsQuoted([]Level{Level("\x1b[31mred"), Level("a\nb")})
	if strings.ContainsAny(got, "\x1b\n") {
		t.Fatalf("raw control bytes reached the rendering: %q", got)
	}
}

// The thinking dialect emits one of two thinking objects, so it cannot carry
// depth. Every other named dialect either sends a level string or pairs one
// with the thinking object.
func TestCanGrade(t *testing.T) {
	for _, dialect := range []Dialect{DialectOpenAI, DialectOpenRouter, DialectThinkingEffort} {
		if !dialect.CanGrade() {
			t.Fatalf("%q carries a level on the wire and must grade", dialect)
		}
	}
	for _, dialect := range []Dialect{DialectThinking, DialectNone, "", Dialect("unheard-of")} {
		if dialect.CanGrade() {
			t.Fatalf("%q sends no level value and must not claim to grade", dialect)
		}
	}
}

// Resolve is the only place that sequences "configured dialect, else the
// provider's vetted default". Every direction matters: a configured dialect
// must survive a provider that has a different default, an unconfigured one on
// a vetted provider must come back filled in, and an unvetted provider must
// stay empty so the caller can refuse rather than guess a wire shape.
func TestResolveFillsTheDialectFromTheVettedDefault(t *testing.T) {
	cases := map[string]struct {
		provider string
		in       Setting
		want     Setting
	}{
		"configured dialect wins": {
			"zai",
			Setting{Level: High, Dialect: DialectThinkingEffort},
			Setting{Level: High, Dialect: DialectThinkingEffort},
		},
		"unconfigured resolves to the provider default": {
			"zai",
			Setting{Level: High},
			Setting{Level: High, Dialect: DialectThinking},
		},
		"unvetted provider stays empty": {
			"kimi",
			Setting{Level: High},
			Setting{Level: High},
		},
		"deepseek resolves to thinking_effort": {
			"deepseek",
			Setting{Level: High},
			Setting{Level: High, Dialect: DialectThinkingEffort},
		},
		"an inactive level still resolves its dialect": {
			"openrouter",
			Setting{},
			Setting{Dialect: DialectOpenAI},
		},
	}
	for label, tc := range cases {
		t.Run(label, func(t *testing.T) {
			if got := Resolve(tc.provider, tc.in); got != tc.want {
				t.Fatalf("Resolve(%q, %+v) = %+v, want %+v", tc.provider, tc.in, got, tc.want)
			}
		})
	}
}

// The provider clients read their default from this table rather than naming a
// dialect of their own. Changing a value here changes what those clients put on
// the wire, which is exactly why there must be only one copy of it.
func TestDefaultDialectCoversTheClientsThatDependOnIt(t *testing.T) {
	for provider, want := range map[string]Dialect{
		"zai": DialectThinking, "openrouter": DialectOpenAI, "deepseek": DialectThinkingEffort,
	} {
		got, ok := DefaultDialect(provider)
		if !ok {
			t.Fatalf("provider %q has no vetted default, but its client reads one", provider)
		}
		if got != want {
			t.Fatalf("DefaultDialect(%q) = %q, want %q", provider, got, want)
		}
	}
}
