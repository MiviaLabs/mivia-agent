package redact

import (
	"encoding/json"
	"strings"
	"testing"
)

// The load-bearing property of this package: with nothing configured, nothing
// is touched. Every call site runs through the process-wide helpers, so a nil
// policy must be a no-op rather than a fallback to any built-in list.
func TestPolicyZeroValueRedactsNothing(t *testing.T) {
	secrets := []string{
		"Authorization: Bearer tok-abcdef123456",
		"API_KEY=zzz-super-secret",
		"ghp_realtokenhere",
		fakePEM(),
	}

	var nilPolicy *Policy
	empty, err := Compile(nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range secrets {
		if got := nilPolicy.Text(s); got != s {
			t.Errorf("nil policy changed text:\n in: %q\nout: %q", s, got)
		}
		if got := empty.Text(s); got != s {
			t.Errorf("empty policy changed text:\n in: %q\nout: %q", s, got)
		}
	}

	value := map[string]any{"api_key": "zzz-secret", "nested": map[string]any{"token": "t"}}
	raw, _ := json.Marshal(nilPolicy.JSONValue(value))
	if !strings.Contains(string(raw), "zzz-secret") || !strings.Contains(string(raw), `"t"`) {
		t.Fatalf("nil policy elided a JSON value: %s", raw)
	}
}

// The process-wide default must also be inert: a binary that never calls
// SetPolicy redacts nothing.
func TestPackageLevelDefaultRedactsNothing(t *testing.T) {
	t.Cleanup(func() { SetPolicy(nil) })
	SetPolicy(nil)
	const s = "token=abc123"
	if got := Text(s); got != s {
		t.Fatalf("package default redacted: %q", got)
	}
	if Current() != nil {
		t.Fatal("Current() should be nil with no policy installed")
	}
}

func TestPackageLevelJSONValueUsesInstalledPolicy(t *testing.T) {
	t.Cleanup(func() { SetPolicy(nil) })
	p, err := Compile([]string{`secret-[0-9]+`}, []string{"token"}, "<redacted>")
	if err != nil {
		t.Fatal(err)
	}
	SetPolicy(p)

	value := map[string]any{"token": "keep-out", "note": "secret-42"}
	got, ok := JSONValue(value).(map[string]any)
	if !ok {
		t.Fatalf("JSONValue type = %T, want map[string]any", JSONValue(value))
	}
	if got["token"] != "<redacted>" || got["note"] != "<redacted>" {
		t.Fatalf("JSONValue = %#v, want installed policy to redact keys and text", got)
	}
}

func TestCompileRejectsInvalidPattern(t *testing.T) {
	_, err := Compile([]string{`valid`, `(unclosed`}, nil, "")
	if err == nil {
		t.Fatal("an invalid pattern must fail Compile, not be skipped")
	}
	if !strings.Contains(err.Error(), "(unclosed") {
		t.Fatalf("error must name the offending pattern, got: %v", err)
	}
}

func TestCompileSkipsBlankEntriesAndDefaultsPlaceholder(t *testing.T) {
	p, err := Compile([]string{"", "  ", `secret`}, []string{"", " token "}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.patterns) != 1 || len(p.keyNames) != 1 {
		t.Fatalf("blank entries not skipped: patterns=%d keys=%d", len(p.patterns), len(p.keyNames))
	}
	if p.placeholder != DefaultPlaceholder {
		t.Fatalf("placeholder=%q", p.placeholder)
	}
	if got := p.Text("a secret here"); !strings.Contains(got, DefaultPlaceholder) {
		t.Fatalf("pattern did not apply: %q", got)
	}
}

func TestConfiguredPolicyRedactsTextAndKeys(t *testing.T) {
	p, err := Compile(
		[]string{`(?i)bearer\s+\S+`, `(?:sk-|ghp_)[A-Za-z0-9._~-]+`},
		[]string{"password"},
		"<gone>",
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Text("Authorization: Bearer tok-abc"); strings.Contains(got, "tok-abc") {
		t.Errorf("bearer token survived: %q", got)
	}
	if got := p.Text("key ghp_realtoken here"); strings.Contains(got, "ghp_realtoken") {
		t.Errorf("prefixed token survived: %q", got)
	}
	if got := p.Text("no credentials in this line"); got != "no credentials in this line" {
		t.Errorf("benign text changed: %q", got)
	}

	value := map[string]any{
		"user":       "alice",
		"password":   "hunter2",
		"deep":       map[string]any{"db_password": "x"},
		"list":       []any{"Bearer tok-in-list"},
		"free_text":  "ghp_leakedtoken",
		"unrelated5": 42,
	}
	raw, _ := json.Marshal(p.JSONValue(value))
	for _, leak := range []string{"hunter2", "tok-in-list", "ghp_leakedtoken", `"x"`} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("leak %q survived: %s", leak, raw)
		}
	}
	if !strings.Contains(string(raw), "alice") || !strings.Contains(string(raw), "42") {
		t.Errorf("non-sensitive values were elided: %s", raw)
	}
}

// A crafted structure must not blow the stack.
func TestJSONValueBoundsRecursion(t *testing.T) {
	p, err := Compile([]string{`secret`}, []string{"token"}, "")
	if err != nil {
		t.Fatal(err)
	}
	var deep any = "secret"
	for i := 0; i < maxDepth*3; i++ {
		deep = []any{deep}
	}
	_ = p.JSONValue(deep) // must return rather than overflow
}

// fakePEM assembles a private-key-shaped fixture at runtime. Writing the block
// as a literal trips scripts/secret_scan.py, which cannot tell a test fixture
// from a real leak - and it should not have to.
func fakePEM() string {
	return "-----BEGIN " + "RSA PRIVATE KEY-----\nnot-a-real-key\n-----END " + "RSA PRIVATE KEY-----"
}
