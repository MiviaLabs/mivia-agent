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

// The parser runs on operator-typed strings of unbounded size. A huge level
// must fail closed with an error, never panic: the contract states no length
// cap, and the exact-match map lookup must not assume one.
func TestParseLevelHandlesOversizedInputNoPanic(t *testing.T) {
	if _, err := ParseLevel(strings.Repeat("x", 4<<20)); err == nil {
		t.Fatal("ParseLevel of a 4 MiB non-vocabulary string must fail")
	}
}

// ParseLevel errors print to stderr and may carry a level exactly as the
// operator typed it, before validation has rejected it. %q must escape a raw
// ESC so the message cannot recolour or clear the reader's terminal.
func TestParseLevelErrorEscapesControlBytes(t *testing.T) {
	_, err := ParseLevel("\x1b[31mred")
	if err == nil {
		t.Fatal("ParseLevel of a control-byte level must fail")
	}
	if strings.ContainsAny(err.Error(), "\x1b") {
		t.Fatalf("raw ESC reached the error text: %q", err)
	}
	if !strings.Contains(err.Error(), `\x1b`) {
		t.Fatalf("error must show the escaped form, got %q", err)
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
	for _, want := range []Dialect{DialectOpenAI, DialectOpenRouter, DialectThinking, DialectThinkingEffort, DialectThinkingPreserved, DialectNone} {
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

// A dialect is spelled exactly as configured, the same rule as levels.
// Accepting a case or space variant here would make the config surface
// silently forgiving in one place and strict in every other closed TOML object.
func TestParseDialectIsCaseAndSpaceSensitive(t *testing.T) {
	for _, bad := range []string{"OPENAI", " openai", "openai "} {
		if _, err := ParseDialect(bad); err == nil {
			t.Fatalf("ParseDialect(%q) must fail", bad)
		}
	}
}

// Same no-panic contract as the level parser: a huge dialect string is a plain
// map miss, never a bound or a slice to trip over.
func TestParseDialectHandlesOversizedInputNoPanic(t *testing.T) {
	if _, err := ParseDialect(strings.Repeat("x", 4<<20)); err == nil {
		t.Fatal("ParseDialect of a 4 MiB non-vocabulary string must fail")
	}
}

// The dialect error also renders operator-typed input, so %q must escape a raw
// ESC there too; a terminal-control byte in a config error is not an error.
func TestParseDialectErrorEscapesControlBytes(t *testing.T) {
	_, err := ParseDialect("\x1b[31mred")
	if err == nil {
		t.Fatal("ParseDialect of a control-byte dialect must fail")
	}
	if strings.ContainsAny(err.Error(), "\x1b") {
		t.Fatalf("raw ESC reached the error text: %q", err)
	}
	if !strings.Contains(err.Error(), `\x1b`) {
		t.Fatalf("error must show the escaped form, got %q", err)
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

// Duplicates render verbatim. The renderer stays dumb: config's
// checkReasoningEfforts rejects a repeated level before any catalog set reaches
// this function, so deduplicating here would hide a config error behind a
// silently "fixed" UI line.
func TestFormatLevelsRendersDuplicatesAsIs(t *testing.T) {
	if got := FormatLevels([]Level{High, High, Low}); got != "high, high, low" {
		t.Fatalf("duplicate set = %q, want every element rendered as-is", got)
	}
}

// The thinking dialect emits one of two thinking objects, so it cannot carry
// depth. Every other named dialect either sends a level string or pairs one
// with the thinking object.
func TestCanGrade(t *testing.T) {
	for _, dialect := range []Dialect{DialectOpenAI, DialectOpenRouter, DialectThinkingEffort, DialectThinkingPreserved} {
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

// The INV-AG-36 invariant once claimed DeepSeek has no default dialect. The
// shipped table vets deepseek to thinking_effort, and every consumer depends on
// that value. This pin machine-checks the contract inside the package that owns
// the table.
func TestDefaultDialectVetsDeepSeek(t *testing.T) {
	got, ok := DefaultDialect("deepseek")
	if !ok {
		t.Fatal("deepseek must have a vetted default dialect")
	}
	if got != DialectThinkingEffort {
		t.Fatalf("DefaultDialect(\"deepseek\") = %q, want %q", got, DialectThinkingEffort)
	}
	parsed, err := ParseDialect(string(DialectThinkingEffort))
	if err != nil {
		t.Fatalf("ParseDialect(%q): unexpected error: %v", DialectThinkingEffort, err)
	}
	if parsed != DialectThinkingEffort {
		t.Fatalf("ParseDialect(%q) = %q, want a round trip", DialectThinkingEffort, parsed)
	}
	if !DialectThinkingEffort.CanGrade() {
		t.Fatal("DialectThinkingEffort carries a level on the wire and must grade")
	}
}

// Resolve is the only place that sequences deepseek's vetted default onto
// requests that do not name a dialect. All three directions must hold: an
// unconfigured level fills in the default, an explicit dialect wins, and an
// inactive level still resolves its dialect so the binding knows the wire shape
// it would carry if dialled up.
func TestResolveSequencesDeepSeekDefault(t *testing.T) {
	cases := []struct {
		label string
		in    Setting
		want  Setting
	}{
		{"an active level resolves the vetted default", Setting{Level: High}, Setting{Level: High, Dialect: DialectThinkingEffort}},
		{"a configured dialect wins", Setting{Level: High, Dialect: DialectOpenAI}, Setting{Level: High, Dialect: DialectOpenAI}},
		{"an inactive level still resolves its dialect", Setting{}, Setting{Dialect: DialectThinkingEffort}},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			if got := Resolve("deepseek", tc.in); got != tc.want {
				t.Fatalf("Resolve(\"deepseek\", %+v) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}

// An explicit DialectNone is a deliberate statement and must win over a
// provider's vetted default. Resolve keeps the pair exactly as written so
// config's explicit-none refusal sees what the operator configured rather than
// a default it would have to undo, and the encoder's nil fall-through sends
// nothing (INV-AG-36).
func TestResolveExplicitNoneWinsOverProviderDefault(t *testing.T) {
	in := Setting{Level: High, Dialect: DialectNone}
	if got := Resolve("deepseek", in); got != in {
		t.Fatalf("Resolve(\"deepseek\", {high, none}) = %+v, want the explicit none kept", got)
	}
}
