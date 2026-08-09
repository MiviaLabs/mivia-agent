package memory

import (
	"strings"
	"testing"
)

func TestNormalizeOrgIDValid(t *testing.T) {
	cases := map[string]string{
		"github.com/MiviaLabs": "github.com/mivialabs",
		"  github.com/Acme  ":  "github.com/acme",
		"GITHUB.COM/My-Org_1":  "github.com/my-org_1",
		"gitlab.example.com/x": "gitlab.example.com/x",
	}
	for in, want := range cases {
		got, err := NormalizeOrgID(in)
		if err != nil {
			t.Errorf("NormalizeOrgID(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("NormalizeOrgID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeOrgIDInvalid(t *testing.T) {
	for _, in := range []string{
		"",
		"   ",
		"has space",
		"a/b/..",
		"..",
		"/leading",
		"trailing/",
		"a..b",
		"tab\there",
		"newline\nhere",
		strings.Repeat("x", 129),
	} {
		if got, err := NormalizeOrgID(in); err == nil {
			t.Errorf("NormalizeOrgID(%q) = %q, want error", in, got)
		}
	}
}

func TestNormalizeOrgIDCaseInsensitiveIdentity(t *testing.T) {
	a, _ := NormalizeOrgID("github.com/MiviaLabs")
	b, _ := NormalizeOrgID("github.com/mivialabs")
	if a != b {
		t.Errorf("org identity must be case-insensitive: %q != %q", a, b)
	}
}
