package config

import (
	"strings"
	"testing"
)

// The model object is a CLOSED shape: a typo must be a hard error, not a
// silently ignored key. These cases drive every rejection branch in the
// decoder, since each one is the difference between a setting that applies and
// one the operator only believes applies.
func TestModelObjectRejectsEveryMalformedShape(t *testing.T) {
	cases := map[string]string{
		"scalar instead of object":  `models = ["deepseek-v4-flash"]`,
		"name not a string":         `models = [{ name = 3, context_window_tokens = 128000 }]`,
		"context not an integer":    `models = [{ name = "m", context_window_tokens = "big" }]`,
		"max output not an integer": `models = [{ name = "m", context_window_tokens = 128000, max_output_tokens = "lots" }]`,
		"unknown key":               `models = [{ name = "m", context_window_tokens = 128000, windows = 5 }]`,
		"max output unparseable":    `models = [{ name = "m", context_window_tokens = 128000, max_output_tokens = 99999999999999999999999 }]`,
		"reasoning not a level":     `models = [{ name = "m", context_window_tokens = 128000, reasoning = "turbo" }]`,
		"dialect not a dialect":     `models = [{ name = "m", context_window_tokens = 128000, reasoning_dialect = "qwen" }]`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			path := writeCatalogConfig(t, `[provider]
name = "deepseek"

[providers.deepseek]
`+body+`

[chat]
max_tokens = 8192
`, "DEEPSEEK_API_KEY=k\n")
			if _, err := Load(LoadOptions{ConfigPath: path}); err == nil {
				t.Fatalf("%s must be rejected", name)
			}
		})
	}
}

// A full-table model entry decodes the same as an inline one; the shape check
// accepts both kinds deliberately.
func TestModelObjectAcceptsAFullTableEntry(t *testing.T) {
	path := writeCatalogConfig(t, `[provider]
name = "deepseek"

[[providers.deepseek.models]]
name = "deepseek-v4-flash"
context_window_tokens = 128000
max_output_tokens = 4096

[chat]
max_tokens = 8192
`, "DEEPSEEK_API_KEY=k\n")
	res, err := Load(LoadOptions{ConfigPath: path})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	spec := profileNamed(t, res, "deepseek", "deepseek-v4-flash")
	if spec.ContextWindowTokens != 128000 || spec.MaxOutputTokens != 4096 {
		t.Fatalf("spec = %+v", spec)
	}
}

// A context window too large for an int is a decode failure, not a range
// failure: it never reaches the range check, so the decoder must reject it
// rather than silently truncating.
func TestModelObjectRejectsAnUnparseableContextWindow(t *testing.T) {
	path := writeCatalogConfig(t, `[provider]
name = "deepseek"

[providers.deepseek]
models = [{ name = "m", context_window_tokens = 99999999999999999999999 }]

[chat]
max_tokens = 8192
`, "DEEPSEEK_API_KEY=k\n")
	_, err := Load(LoadOptions{ConfigPath: path})
	if err == nil || !strings.Contains(err.Error(), "invalid model object") {
		t.Fatalf("error = %v, want an invalid-object rejection", err)
	}
}
