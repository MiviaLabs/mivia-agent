package stringutil

import "testing"

// TestToKebabCase covers camelCase, PascalCase, snake_case, SCREAMING_SNAKE,
// already-kebab-case input, separator runs, empty input, digits, and non-ASCII
// letters.
func TestToKebabCase(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"single lower word", "foo", "foo"},
		{"single upper word", "Foo", "foo"},
		{"camelCase", "camelCase", "camel-case"},
		{"camelCase acronym end", "parseURL", "parse-url"},
		{"PascalCase", "CamelCase", "camel-case"},
		{"snake_case", "snake_case", "snake-case"},
		{"SCREAMING_SNAKE", "SNAKE_CASE", "snake-case"},
		{"already kebab", "already-kebab", "already-kebab"},
		{"leading acronym run", "HTTPServer", "http-server"},
		{"two acronym runs", "XMLToJSON", "xml-to-json"},
		{"spaces", "Hello World", "hello-world"},
		{"collapsed separators", "foo__bar--baz", "foo-bar-baz"},
		{"leading separator", "_foo", "foo"},
		{"trailing separator", "foo_", "foo"},
		{"only separators", "_ -", ""},
		{"digits stay in word", "v2beta1", "v2beta1"},
		{"upper after digit", "foo2Bar", "foo2-bar"},
		{"mixed separators", "Mixed_SNAKE_case", "mixed-snake-case"},
		{"non-ASCII letters", "ÜberCafé", "über-café"},
		{"titlecase letter", "ǅ", "ǆ"},
		{"titlecase followed by lower", "ǅz", "ǆz"},
		{"titlecase mid word", "foǅBar", "fo-ǆ-bar"},
		{"titlecase before separator", "ǅ-b", "ǆ-b"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ToKebabCase(c.in); got != c.want {
				t.Errorf("ToKebabCase(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
