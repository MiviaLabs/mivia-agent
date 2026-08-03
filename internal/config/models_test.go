package config

import (
	"strings"
	"testing"
)

// A model name reaches terminal output and the provider URL path, so every
// rejection reason has to hold, not just the empty one.
func TestNormalizeModelNameRejections(t *testing.T) {
	cases := map[string]string{
		"empty":            "",
		"blank":            "   ",
		"invalid utf-8":    "bad\xff\xfename",
		"control sequence": "model\x1b]52;c;steal",
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := NormalizeModelName(input)
			if err == nil {
				t.Fatalf("%q was accepted as %q", input, got)
			}
			// The value is deliberately omitted: it is attacker-influenced and
			// this error is printed to a terminal.
			if strings.Contains(err.Error(), input) && input != "" {
				t.Fatalf("error echoed the rejected value: %v", err)
			}
		})
	}
}

func TestNormalizeModelsRejections(t *testing.T) {
	oversized := make([]ModelSpec, maxProviderModels+1)
	for i := range oversized {
		oversized[i] = ModelSpec{Name: string(rune('a' + i%26)), ContextWindowTokens: 128000}
	}
	cases := []struct {
		name      string
		in        []ModelSpec
		maxTokens int
		want      string
	}{
		{"too many entries", oversized, 0, "too many entries"},
		{"empty name", []ModelSpec{{ContextWindowTokens: 128000}}, 0, "models[0] is empty"},
		{"invalid name", []ModelSpec{{Name: "bad\x1bname", ContextWindowTokens: 128000}}, 0, "models[0] is invalid"},
		{"window below floor", []ModelSpec{{Name: "m", ContextWindowTokens: 10}}, 0, "invalid context window"},
		{"window above ceiling", []ModelSpec{{Name: "m", ContextWindowTokens: maxContextWindowTokens + 1}}, 0, "invalid context window"},
		{"output exceeds window", []ModelSpec{{Name: "m", ContextWindowTokens: 2048, MaxOutputTokens: 4096}}, 0, "invalid max output tokens"},
		{"window too small for max_tokens", []ModelSpec{{Name: "m", ContextWindowTokens: 2048}}, 4096, "too small for max_tokens"},
		{"duplicate", []ModelSpec{
			{Name: "m", ContextWindowTokens: 128000},
			{Name: "m", ContextWindowTokens: 128000},
		}, 0, "models[1] is a duplicate"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := normalizeModels(tc.in, tc.maxTokens, "zai")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want one containing %q", err, tc.want)
			}
		})
	}
}

func TestNormalizeModelsAcceptsAnEmptyCatalog(t *testing.T) {
	out, err := normalizeModels(nil, 0, "zai")
	if err != nil || out != nil {
		t.Fatalf("out=%v err=%v, want nil/nil", out, err)
	}
}
