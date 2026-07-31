package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadWith(t *testing.T, privacy string) (*Resolved, error) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "mivia.toml")
	body := "[provider]\nname = \"deepseek\"\n\n[providers.deepseek]\nmodels = [{name=\"deepseek-v4-flash\", context_window_tokens=128000}]\n\n[chat]\nmax_tokens = 8192\n\n" + privacy
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(LoadOptions{ConfigPath: path})
}

// A workspace that configures no patterns gets no policy, and therefore no
// redaction anywhere. This is the documented default (plan 10 §5).
func TestLoadWithoutPrivacySectionRedactsNothing(t *testing.T) {
	res, err := loadWith(t, "")
	if err != nil {
		t.Fatal(err)
	}
	const secret = "Authorization: Bearer tok-abcdef"
	if got := res.RedactionPolicy.Text(secret); got != secret {
		t.Fatalf("unconfigured workspace redacted: %q", got)
	}
}

func TestLoadCompilesConfiguredPatterns(t *testing.T) {
	res, err := loadWith(t, "[privacy]\nredaction_patterns = ['(?i)bearer\\s+\\S+']\nredaction_key_names = [\"password\"]\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := res.RedactionPolicy.Text("Authorization: Bearer tok-abcdef"); strings.Contains(got, "tok-abcdef") {
		t.Fatalf("configured pattern did not apply: %q", got)
	}
}

// A malformed pattern must stop startup and name itself. Dropping it silently
// would leave an operator believing they are covered when they are not.
func TestLoadRejectsInvalidRedactionPattern(t *testing.T) {
	_, err := loadWith(t, "[privacy]\nredaction_patterns = [\"(unclosed\"]\n")
	if err == nil {
		t.Fatal("invalid redaction pattern must fail config load")
	}
	if !strings.Contains(err.Error(), "(unclosed") {
		t.Fatalf("error must name the pattern, got: %v", err)
	}
}
