package secretpath

import "testing"

func TestNewMatchesConfiguredRules(t *testing.T) {
	policy, err := New([]string{".ENV", "id_rsa"}, []string{".env.example"})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{".env", "config.env.backup", "keys/ID_RSA.pub"} {
		if !policy.Match(path) {
			t.Fatalf("Match(%q) = false, want true", path)
		}
	}
	for _, path := range []string{".env.example", "main.go"} {
		if policy.Match(path) {
			t.Fatalf("Match(%q) = true, want false", path)
		}
	}
}

func TestNewRejectsUnsafeException(t *testing.T) {
	for _, exception := range []string{"", "/.env.example", "../.env.example"} {
		t.Run(exception, func(t *testing.T) {
			if _, err := New([]string{".env"}, []string{exception}); err == nil {
				t.Fatal("New() error = nil, want error")
			}
		})
	}
}

// TestMatchNormalizesConfiguredPatterns pins the DC-11 regression: patterns are
// stored in the same canonical form (lowercase, forward slashes, Clean) that Match
// derives from the rel, so a configured pattern with a leading "./", a trailing "/",
// a "." / ".." segment, or Windows backslashes matches the cleaned workspace-relative
// path instead of silently failing open.
func TestMatchNormalizesConfiguredPatterns(t *testing.T) {
	policy, err := New([]string{"./id_rsa", ".env/", "config/../creds", "./ID_RSA"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"id_rsa", ".env", "creds/file", "keys/id_rsa"} {
		if !policy.Match(rel) {
			t.Fatalf("Match(%q) = false, want true", rel)
		}
	}
}

func TestNewPatternBoundaries(t *testing.T) {
	// (a) whitespace-only pattern is still rejected; the TrimSpace + empty check must
	// run before normalization because filepath.Clean("") == ".".
	if _, err := New([]string{"   "}, nil); err == nil {
		t.Fatal(`New(["   "]) error = nil, want error`)
	}

	// (b) a pattern with leading/trailing whitespace still matches (normalizePath trims).
	trimmed, err := New([]string{".env "}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !trimmed.Match(".env") {
		t.Fatal(`Match(".env") = false, want true for trimmed pattern`)
	}

	// (c) exceptions still win after pattern normalization (exact exception beats a
	// substring pattern).
	exceptions, err := New([]string{"./id_rsa"}, []string{"id_rsa.example"})
	if err != nil {
		t.Fatal(err)
	}
	if exceptions.Match("id_rsa.example") {
		t.Fatal(`Match("id_rsa.example") = true, want false (exception wins)`)
	}
	if !exceptions.Match("id_rsa") {
		t.Fatal(`Match("id_rsa") = false, want true`)
	}

	// (d) case-insensitivity is preserved after normalization.
	ci, err := New([]string{"./ID_RSA"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !ci.Match("keys/id_rsa") {
		t.Fatal(`Match("keys/id_rsa") = false, want true (case-insensitive)`)
	}

	// (e) a zero-value policy (no patterns) matches nothing.
	zero, err := New(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{".env", "id_rsa", "creds/file"} {
		if zero.Match(rel) {
			t.Fatalf("zero-value Match(%q) = true, want false", rel)
		}
	}

	// (f) duplicate patterns normalize to one form and are accepted without error.
	dup, err := New([]string{".env", "./.env"}, nil)
	if err != nil {
		t.Fatalf("New with duplicate patterns: %v", err)
	}
	if !dup.Match(".env") {
		t.Fatal(`Match(".env") = false, want true`)
	}

	// (g) empty, ".", "..", and "a/.." rels produce no false positives.
	policy, err := New([]string{"./id_rsa", ".env/", "config/../creds"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"", ".", "..", "a/.."} {
		if policy.Match(rel) {
			t.Fatalf("Match(%q) = true, want false", rel)
		}
	}
}
