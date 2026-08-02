package config

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A misspelled model key must be rejected however the entry is spelled.
// go-toml dispatches ModelSpec.UnmarshalTOML for inline tables only, so an
// array-of-tables entry used to reach normalizeModels with the typo already
// discarded: the model silently offered no reasoning and /effort showed an
// empty picker, while the identical inline config was a hard error.
func TestUnknownModelKeyIsRejectedInBothSpellings(t *testing.T) {
	spellings := map[string]string{
		"array of tables": `[[providers.zai.models]]
name = "glm-5.2"
context_window_tokens = 1000000
reasoning_effots = ["low", "high"]
reasoning_dialect = "thinking_effort"
`,
		"inline table": `[providers.zai]
models = [{ name = "glm-5.2", context_window_tokens = 1000000, reasoning_effots = ["low", "high"], reasoning_dialect = "thinking_effort" }]
`,
	}
	for name, body := range spellings {
		t.Run(name, func(t *testing.T) {
			path := writeCatalogConfig(t, `[provider]
name = "zai"

`+body+`
[chat]
max_tokens = 8192
`, "ZAI_API_KEY=k\n")
			_, err := Load(LoadOptions{ConfigPath: path})
			if err == nil {
				t.Fatal("a misspelled model key must be rejected")
			}
			if !strings.Contains(err.Error(), `"reasoning_effots"`) {
				t.Fatalf("error must name the unknown key, got: %v", err)
			}
		})
	}
}

// The rejection must name where the key was found: a catalog can hold many
// providers and many entries, and "unknown key" without a location leaves the
// operator diffing the whole file.
func TestUnknownModelKeyErrorLocatesTheEntry(t *testing.T) {
	path := writeCatalogConfig(t, `[provider]
name = "deepseek"

[[providers.deepseek.models]]
name = "deepseek-v4-flash"
context_window_tokens = 128000

[[providers.deepseek.models]]
name = "deepseek-v4-thinking"
context_window_tokens = 128000
reasonning = "high"

[chat]
max_tokens = 8192
`, "DEEPSEEK_API_KEY=k\n")
	_, err := Load(LoadOptions{ConfigPath: path})
	if err == nil {
		t.Fatal("a misspelled model key must be rejected")
	}
	msg := err.Error()
	for _, want := range []string{`"deepseek"`, "models[1]", `"reasonning"`} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error must mention %s, got: %v", want, err)
		}
	}
}

// Config values reach the terminal through cmd/mivia/main.go, so a key carrying
// an escape sequence must not be echoed raw.
func TestUnknownModelKeyErrorEscapesControlBytes(t *testing.T) {
	path := writeCatalogConfig(t, `[provider]
name = "deepseek"

[[providers.deepseek.models]]
name = "deepseek-v4-flash"
context_window_tokens = 128000
"\u001B[31mred" = 1

[chat]
max_tokens = 8192
`, "DEEPSEEK_API_KEY=k\n")
	_, err := Load(LoadOptions{ConfigPath: path})
	if err == nil {
		t.Fatal("a misspelled model key must be rejected")
	}
	if strings.Contains(err.Error(), "\x1b") {
		t.Fatalf("error echoes a raw escape byte: %q", err.Error())
	}
}

// A realistic entry using every key must load through the whole pipeline,
// cross-field model rules included. Drift between the decoder map and the
// struct tags is checked per key in model_key_drift_test.go.
func TestKnownModelKeysAreAcceptedByBothPaths(t *testing.T) {
	body := `[provider]
name = "deepseek"

[[providers.deepseek.models]]
name = "deepseek-v4-thinking"
context_window_tokens = 128000
max_output_tokens = 4096
reasoning = "high"
reasoning_efforts = ["low", "high"]
reasoning_dialect = "thinking_effort"

[chat]
max_tokens = 8192
`
	path := writeCatalogConfig(t, body, "DEEPSEEK_API_KEY=k\n")
	if _, err := Load(LoadOptions{ConfigPath: path}); err != nil {
		t.Fatalf("every known model key must load: %v", err)
	}
}

// The shipped config and the example are what every workspace runs and copies;
// the audit runs on both of them on every load.
func TestShippedConfigsPassTheModelKeyAudit(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	for _, name := range []string{"mivia.toml", "mivia.toml.example"} {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(LoadOptions{ConfigPath: filepath.Join(root, ".mivia", name)}); err != nil {
				t.Fatalf("shipped config %s does not load: %v", name, err)
			}
		})
	}
}

// The audit exists to name the mistyped key. A value the audit view cannot
// carry must not pre-empt that diagnosis: an operator told the checker broke
// has no way to find the typo that is sitting in the same entry.
func TestUnknownModelKeyIsNamedDespiteAnUndecodableValue(t *testing.T) {
	path := writeCatalogConfig(t, `[provider]
name = "zai"

[[providers.zai.models]]
name = "m"
context_window_tokens = 1
typo = 99999999999999999999

[chat]
max_tokens = 8192
`, "ZAI_API_KEY=k\n")
	_, err := Load(LoadOptions{ConfigPath: path})
	if err == nil {
		t.Fatal("a misspelled model key must be rejected")
	}
	if !strings.Contains(err.Error(), `unknown model key "typo"`) {
		t.Fatalf("error must name the unknown key, got: %v", err)
	}
}

// The audit runs after the strict decode, so a document reaching it always
// parsed once. Its own view failing anyway means the providers region is a
// shape this check cannot read, and an unauditable catalog must not pass as an
// audited one - so the branch fails closed rather than waving the file through.
func TestModelKeyAuditFailsClosedOnAnUnreadableProvidersRegion(t *testing.T) {
	err := auditModelKeys([]byte("providers = 5\n"))
	if err == nil {
		t.Fatal("an unreadable providers region must not pass the audit")
	}
	if !strings.Contains(err.Error(), "cannot be checked") {
		t.Fatalf("error = %v, want it to say the keys could not be checked", err)
	}
}
