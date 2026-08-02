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

// A table nested under a model entry is a key the closed model object does not
// define, written as a header instead of an assignment. It must be named like
// any other typo.
func TestUnknownModelKeyIsNamedWhenWrittenAsANestedTable(t *testing.T) {
	path := writeCatalogConfig(t, `[provider]
name = "zai"

[[providers.zai.models]]
name = "m"
context_window_tokens = 1

[[providers.zai.models.tiers]]
price = 1

[chat]
max_tokens = 8192
`, "ZAI_API_KEY=k\n")
	_, err := Load(LoadOptions{ConfigPath: path})
	if err == nil {
		t.Fatal("a table nested under a model entry must be rejected")
	}
	if !strings.Contains(err.Error(), `unknown model key "tiers"`) {
		t.Fatalf("error must name the unknown key, got: %v", err)
	}
}

// One catalog region the audit cannot describe must not silence the audit of
// every other region: an operator whose typo is reported only once the
// unrelated entry is fixed has to guess twice.
func TestUnknownModelKeySurvivesANestedTableElsewhereInTheDocument(t *testing.T) {
	body := `[provider]
name = "deepseek"

[[providers.deepseek.models]]
name = "m"
context_window_tokens = 1
typo = 1

[[providers.zai.models]]
name = "n"
context_window_tokens = 1

[[providers.zai.models.tiers]]
price = 1
`
	if err := auditModelKeys([]byte(body)); err == nil {
		t.Fatal("a misspelled model key must be rejected")
	} else if !strings.Contains(err.Error(), `unknown model key "typo"`) {
		t.Fatalf("error must name the unrelated key, got: %v", err)
	}
}

// A dotted key addresses a nested table the closed model object does not have.
// Reading only its first part would report a key the operator did write as the
// mistake, so the whole path is rejected.
func TestDottedKeyUnderAModelEntryIsRejected(t *testing.T) {
	body := `[[providers.zai.models]]
name = "m"
context_window_tokens = 1
name.bogus = 1
`
	if err := auditModelKeys([]byte(body)); err == nil {
		t.Fatal("a dotted key under a model entry must be rejected")
	} else if !strings.Contains(err.Error(), `"name.bogus"`) {
		t.Fatalf("error must name the whole key path, got: %v", err)
	}
}

// A model entry written inline is audited through the same key paths, dotted
// keys included.
func TestDottedKeyInAnInlineModelEntryIsRejected(t *testing.T) {
	body := `[providers.zai]
models = [{ name = "m", context_window_tokens = 1 }, { name.bogus = 1 }]
`
	err := auditModelKeys([]byte(body))
	if err == nil {
		t.Fatal("a dotted key under a model entry must be rejected")
	}
	for _, want := range []string{"models[1]", `"name.bogus"`} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error must mention %s, got: %v", want, err)
		}
	}
}

// The audit runs after the strict decode, which parses the same bytes with the
// same parser, so bytes reaching the audit are always valid TOML. Failing to
// parse them anyway would mean the two disagree about what the document is, and
// a catalog nobody can read must not pass as an audited one - so the branch
// fails closed rather than waving the file through.
func TestModelKeyAuditFailsClosedOnBytesItCannotParse(t *testing.T) {
	err := auditModelKeys([]byte("[providers.zai\n"))
	if err == nil {
		t.Fatal("unparseable bytes must not pass the audit")
	}
	if !strings.Contains(err.Error(), "cannot be checked") {
		t.Fatalf("error = %v, want it to say the keys could not be checked", err)
	}
}
