package delivery

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writePRTitlePolicy writes TOML content to .mivia/policy/pr-title.toml under
// root, mirroring the commit-message policy test helper.
func writePRTitlePolicy(t *testing.T, root, content string) {
	t.Helper()
	writePRTitlePolicyAt(t, root, ".mivia/policy/pr-title.toml", content)
}

// writePRTitlePolicyAt writes TOML content to relPath under root, so a test
// can plant a policy at a workflow-declared custom pr_title_policy path.
func writePRTitlePolicyAt(t *testing.T, root, relPath, content string) {
	t.Helper()
	dir := filepath.Join(root, filepath.Dir(relPath))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(root, relPath), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", relPath, err)
	}
}

// TestLoadPRTitlePolicy pins the loader contract: an absent file validates
// nothing, and every config defect is a permanent RefusalError.
func TestLoadPRTitlePolicy(t *testing.T) {
	t.Run("absent file returns nil policy", func(t *testing.T) {
		assertPRTitleLoadNil(t, t.TempDir())
	})

	t.Run("malformed TOML is a refusal", func(t *testing.T) {
		assertPRTitleRefusal(t, "title = [unclosed", "with malformed TOML")
	})

	t.Run("unknown field is rejected by strict decode", func(t *testing.T) {
		assertPRTitleRefusal(t, `[title]
pattern = "^.+$"
min_chars = 1
max_chars = 100
unknown_key = true
`, "with unknown field")
	})

	t.Run("bad pattern is a refusal", func(t *testing.T) {
		assertPRTitleRefusal(t, "[title]\npattern = \"([unclosed\"\n", "with bad pattern")
	})

	t.Run("pattern over 2048 bytes is a refusal", func(t *testing.T) {
		content := "[title]\npattern = \"" + strings.Repeat("a", 2049) + "\"\n"
		assertPRTitleRefusal(t, content, "with 2049-byte pattern")
	})

	t.Run("min greater than max is a refusal", func(t *testing.T) {
		assertPRTitleRefusals(t, []string{
			"[title]\nmin_chars = 5\nmax_chars = 3\n",
			"[summary]\nmin_chars = 5\nmax_chars = 3\n",
			"[summary]\nmin_sentences = 5\nmax_sentences = 3\n",
		})
	})

	t.Run("whitespace-only scope is a refusal", func(t *testing.T) {
		assertPRTitleRefusal(t, "[title]\nscopes = [\"core\", \"   \"]\n", "with blank scope")
	})

	t.Run("unreadable policy file is a refusal", func(t *testing.T) {
		assertPRTitleUnreadable(t)
	})

	t.Run("valid policy loads", func(t *testing.T) {
		root := t.TempDir()
		writePRTitlePolicy(t, root, validPRTitlePolicyTOML)
		assertPRTitlePopulated(t, root)
	})

	t.Run("custom path loads when present", func(t *testing.T) {
		root := t.TempDir()
		writePRTitlePolicyAt(t, root, "policy/pr-title.toml", validPRTitlePolicyTOML)
		pol, err := LoadPRTitlePolicy(root, "policy/pr-title.toml")
		if err != nil {
			t.Fatalf("LoadPRTitlePolicy with present custom path = %v, want nil", err)
		}
		if pol == nil || pol.Title.Pattern == "" || len(pol.Title.Scopes) != 2 {
			t.Fatalf("LoadPRTitlePolicy custom path = %+v, want populated policy", pol)
		}
	})
}

// TestLoadPRTitlePolicyCustomPathMissing pins the loader asymmetry: a
// caller-declared EXPLICIT custom policy path that does not exist is a config
// error (a declared file that is missing), so the loader returns a permanent
// RefusalError naming the declared file — unlike a missing DEFAULT policy,
// which validates nothing and returns (nil, nil).
func TestLoadPRTitlePolicyCustomPathMissing(t *testing.T) {
	root := t.TempDir()
	_, err := LoadPRTitlePolicy(root, "policy/pr-title.toml")
	if err == nil || !IsRefusal(err) {
		t.Fatalf("LoadPRTitlePolicy with missing custom path = %v, want RefusalError", err)
	}
	if !strings.Contains(err.Error(), "policy/pr-title.toml") {
		t.Fatalf("LoadPRTitlePolicy err = %q, want the declared custom path named", err.Error())
	}
}

// validPRTitlePolicyTOML is a policy that satisfies every loader rule.
const validPRTitlePolicyTOML = `[title]
pattern = "^(?P<scope>[a-z]+): .+$"
min_chars = 1
max_chars = 100
scopes = ["core", "cli"]

[summary]
required = true
min_chars = 10
max_chars = 1000
min_sentences = 1
max_sentences = 5
`

// assertPRTitleLoadNil asserts the loader returns a nil policy without error.
func assertPRTitleLoadNil(t *testing.T, root string) {
	t.Helper()
	pol, err := LoadPRTitlePolicy(root, "")
	if err != nil {
		t.Fatalf("LoadPRTitlePolicy with absent file = %v, want nil", err)
	}
	if pol != nil {
		t.Fatalf("LoadPRTitlePolicy = %+v, want nil policy", pol)
	}
}

// assertPRTitleRefusal writes content as the PR title policy. It asserts the
// loader rejects it with a RefusalError.
func assertPRTitleRefusal(t *testing.T, content, want string) {
	t.Helper()
	root := t.TempDir()
	writePRTitlePolicy(t, root, content)
	_, err := LoadPRTitlePolicy(root, "")
	if err == nil || !IsRefusal(err) {
		t.Fatalf("LoadPRTitlePolicy %s = %v, want RefusalError", want, err)
	}
}

// assertPRTitleRefusals asserts the loader rejects every content string with
// a RefusalError.
func assertPRTitleRefusals(t *testing.T, contents []string) {
	t.Helper()
	for _, content := range contents {
		root := t.TempDir()
		writePRTitlePolicy(t, root, content)
		if _, err := LoadPRTitlePolicy(root, ""); err == nil || !IsRefusal(err) {
			t.Errorf("LoadPRTitlePolicy with %q = %v, want RefusalError", content, err)
		}
	}
}

// assertPRTitleUnreadable asserts the loader rejects a policy path that is
// not a regular file.
func assertPRTitleUnreadable(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, ".mivia", "policy")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.Mkdir(filepath.Join(dir, "pr-title.toml"), 0o755); err != nil {
		t.Fatalf("mkdir pr-title.toml: %v", err)
	}
	_, err := LoadPRTitlePolicy(root, "")
	if err == nil || !IsRefusal(err) {
		t.Fatalf("LoadPRTitlePolicy with directory at policy path = %v, want RefusalError", err)
	}
}

// assertPRTitlePopulated asserts the loader returns the canonical populated
// policy for root.
func assertPRTitlePopulated(t *testing.T, root string) {
	t.Helper()
	pol, err := LoadPRTitlePolicy(root, "")
	if err != nil {
		t.Fatalf("LoadPRTitlePolicy = %v, want nil", err)
	}
	if pol == nil {
		t.Fatal("LoadPRTitlePolicy = nil, want populated policy")
	}
	if pol.Title.Pattern == "" || pol.Title.MinChars != 1 || pol.Title.MaxChars != 100 || len(pol.Title.Scopes) != 2 {
		t.Errorf("loaded title rule = %+v, want populated", pol.Title)
	}
	if !pol.Summary.Required || pol.Summary.MinChars != 10 || pol.Summary.MaxSentences != 5 {
		t.Errorf("loaded summary rule = %+v, want populated", pol.Summary)
	}
}

// TestPRTitlePolicyValidate pins the deterministic Validate order and the
// PRMetadataError contract. Every violation hint names the field to fix
// (pr_title or pr_summary) and the violated rule value.
func TestPRTitlePolicyValidate(t *testing.T) {
	t.Run("no policy rules passes", func(t *testing.T) {
		assertValidatePasses(t, &PRTitlePolicy{}, "Add feature", "Adds the feature.", "with no rules")
	})

	t.Run("empty title is a metadata error", func(t *testing.T) {
		assertValidateMetadataError(t, &PRTitlePolicy{}, "  ", "Adds the feature.", "pr_title")
	})

	t.Run("regex miss names the pattern", func(t *testing.T) {
		assertValidateMetadataError(t, &PRTitlePolicy{Title: TitleRule{Pattern: `^[a-z]+: .+$`}}, "Add feature", "Adds the feature.", `^[a-z]+: .+$`, "pr_title")
	})

	t.Run("regex hit passes", func(t *testing.T) {
		assertValidatePasses(t, &PRTitlePolicy{Title: TitleRule{Pattern: `^[a-z]+: .+$`}}, "feat: add parsing", "Adds parsing.", "with matching title")
	})

	t.Run("scope membership in-list passes", func(t *testing.T) {
		assertValidatePasses(t, &PRTitlePolicy{Title: TitleRule{Pattern: `^(?P<scope>[a-z]+): .+$`, Scopes: []string{"core", "cli"}}}, "core: add parsing", "Adds parsing.", "with allowed scope")
	})

	t.Run("scope membership out-of-list names the scope", func(t *testing.T) {
		assertValidateMetadataError(t, &PRTitlePolicy{Title: TitleRule{Pattern: `^(?P<scope>[a-z]+): .+$`, Scopes: []string{"core"}}}, "docs: add parsing", "Adds parsing.", "docs", "pr_title")
	})

	t.Run("title rune bounds at 0, 1, max-1, max, max+1", func(t *testing.T) {
		assertTitleRuneBounds(t, &PRTitlePolicy{Title: TitleRule{MinChars: 1, MaxChars: 3}})
	})

	t.Run("rune bounds count runes not bytes", func(t *testing.T) {
		p := &PRTitlePolicy{Title: TitleRule{MinChars: 4, MaxChars: 5}}
		// "héllo" is 5 runes and 6 bytes. "héllo!" is 6 runes.
		assertValidatePasses(t, p, "héllo", "Adds parsing.", "5-rune title")
		assertValidateMetadataError(t, p, "héllo!", "Adds parsing.", "max_chars")
	})

	t.Run("summary required and missing", func(t *testing.T) {
		p := &PRTitlePolicy{Summary: SummaryRule{Required: true}}
		assertValidateMetadataError(t, p, "feat: add parsing", "   ", "pr_summary")
		assertValidatePasses(t, p, "feat: add parsing", "Adds parsing.", "with present summary")
	})

	t.Run("sentence bounds: 2 ok, 1 and 3 fail", func(t *testing.T) {
		p := &PRTitlePolicy{Summary: SummaryRule{MinSentences: 2, MaxSentences: 2}}
		assertValidatePasses(t, p, "feat: x", "Adds v1.2.3 parsing. Adds tests.", "with 2 sentences")
		assertValidateMetadataError(t, p, "feat: x", "Adds parsing.", "pr_summary", "min_sentences")
		assertValidateMetadataError(t, p, "feat: x", "U.S. policy changed. Tests added. Third sentence.", "pr_summary", "max_sentences")
	})

	t.Run("hint names the field and the violated rule value", func(t *testing.T) {
		assertValidateMetadataError(t, &PRTitlePolicy{Title: TitleRule{MinChars: 10}}, "short", "Adds parsing.", "pr_title", "min_chars", "10", "5")
	})
}

// assertValidatePasses asserts the policy accepts the title and summary.
func assertValidatePasses(t *testing.T, p *PRTitlePolicy, title, summary, what string) {
	t.Helper()
	if err := p.Validate(title, summary); err != nil {
		t.Fatalf("Validate %s = %v, want nil", what, err)
	}
}

// assertValidateMetadataError asserts the policy rejects the title and
// summary with a PRMetadataError. The error must not be a RefusalError. Its
// message must contain every hint.
func assertValidateMetadataError(t *testing.T, p *PRTitlePolicy, title, summary string, hints ...string) {
	t.Helper()
	err := p.Validate(title, summary)
	if !IsPRMetadataError(err) {
		t.Fatalf("Validate = %v, want PRMetadataError", err)
	}
	if IsRefusal(err) {
		t.Fatalf("Validate = %v, must not be RefusalError", err)
	}
	for _, hint := range hints {
		if !strings.Contains(err.Error(), hint) {
			t.Errorf("hint %q should contain %q", err.Error(), hint)
		}
	}
}

// assertTitleRuneBounds runs the boundary cases for the title rune limits.
func assertTitleRuneBounds(t *testing.T, p *PRTitlePolicy) {
	t.Helper()
	cases := []struct {
		title   string
		wantErr bool
	}{
		{"", true},     // empty title
		{"a", false},   // 1 rune = min
		{"ab", false},  // 2 runes = max-1
		{"abc", false}, // 3 runes = max
		{"abcd", true}, // 4 runes = max+1
	}
	for _, c := range cases {
		err := p.Validate(c.title, "Adds parsing.")
		if (err != nil) != c.wantErr {
			t.Errorf("Validate(%q) = %v, wantErr %v", c.title, err, c.wantErr)
		}
		if err != nil && !IsPRMetadataError(err) {
			t.Errorf("Validate(%q) = %T, want PRMetadataError", c.title, err)
		}
	}
}

// TestSentenceCount pins the deterministic sentence-boundary rule: a
// terminator followed by whitespace and an uppercase letter, or by end of
// text, counts as a boundary. Abbreviations and version numbers do not split
// sentences.
func TestSentenceCount(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"   ", 0},
		{"Adds parsing.", 1},
		{"Adds parsing!", 1},
		{"Adds v1.2.3 parsing. Adds tests.", 2},
		{"U.S. policy changed. Tests added.", 2},
		{"Use e.g. terms. Then continue.", 2},
		{"First. Second! Third?", 3},
	}
	for _, c := range cases {
		if got := SentenceCount(c.in); got != c.want {
			t.Errorf("SentenceCount(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

// TestLoadPRTitlePolicyExplicitDeclarationMissing pins the declared-path
// asymmetry on DECLARATION PRESENCE, not on string equality with the default:
// a workflow that declares the default path explicitly has opted in, so a
// missing declared file is a RefusalError, while an undeclared (empty) path
// with a missing default file is the legacy no-policy case.
func TestLoadPRTitlePolicyExplicitDeclarationMissing(t *testing.T) {
	root := t.TempDir()

	if pol, err := LoadPRTitlePolicy(root, ""); err != nil || pol != nil {
		t.Fatalf("LoadPRTitlePolicy(root, \"\") with no policy file = (%v, %v), want (nil, nil)", pol, err)
	}
	pol, err := LoadPRTitlePolicy(root, DefaultPRTitlePolicyPath)
	if err == nil {
		t.Fatalf("LoadPRTitlePolicy(root, %q) with no file = (%v, nil), want RefusalError for the explicit declaration", DefaultPRTitlePolicyPath, pol)
	}
	if !IsRefusal(err) {
		t.Fatalf("LoadPRTitlePolicy(root, %q) err = %T, want RefusalError", DefaultPRTitlePolicyPath, err)
	}
	if !strings.Contains(err.Error(), DefaultPRTitlePolicyPath) {
		t.Fatalf("RefusalError = %q, want it to name the declared file", err)
	}
}

// TestIsPRMetadataError pins the type predicate: only PRMetadataError (and
// wrapped copies) match; refusals and plain errors do not.
func TestIsPRMetadataError(t *testing.T) {
	if !IsPRMetadataError(&PRMetadataError{Reason: "x"}) {
		t.Error("IsPRMetadataError should return true for PRMetadataError")
	}
	if IsPRMetadataError(fmt.Errorf("plain")) {
		t.Error("IsPRMetadataError should return false for a plain error")
	}
	if IsPRMetadataError(&RefusalError{Reason: "x"}) {
		t.Error("IsPRMetadataError should return false for RefusalError")
	}
	if IsPRMetadataError(nil) {
		t.Error("IsPRMetadataError should return false for nil")
	}
}
