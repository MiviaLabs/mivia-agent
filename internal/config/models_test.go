package config

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
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

// TestCheckOutputReserveFloorRejectsAConfiguredCeilingBelowTheFloor covers Fix
// B: anthropicMaxTokens (internal/provider/anthropic.go) falls back to
// reasoning.OutputReserveFloor(level) as the wire max_tokens whenever a
// request leaves MaxTokens unset. Without this check, a model entry could
// declare a max_output_tokens ceiling lower than the floor its own
// reasoning_efforts can select, and the client would ask the provider for
// more completion tokens than the operator's own config permits for this
// model.
func TestCheckOutputReserveFloorRejectsAConfiguredCeilingBelowTheFloor(t *testing.T) {
	// reasoning.High's floor is 32768 (internal/reasoning.OutputReserveFloor).
	// 16384 sits strictly below it.
	in := []ModelSpec{{
		Name:                "m",
		ContextWindowTokens: 128000,
		MaxOutputTokens:     16384,
		ReasoningEfforts:    []reasoning.Level{reasoning.High},
	}}
	_, err := normalizeModels(in, 0, "zai")
	if err == nil {
		t.Fatal("want an error, got nil")
	}
	for _, want := range []string{"m", "high", "32768", "16384"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %v, want it to name %q", err, want)
		}
	}
}

// TestCheckOutputReserveFloorAcceptsTheExactBoundary is the boundary case: the
// gate is strict "<", so max_output_tokens exactly equal to the floor for
// every declared effort must PASS, not fail.
func TestCheckOutputReserveFloorAcceptsTheExactBoundary(t *testing.T) {
	in := []ModelSpec{{
		Name:                "m",
		ContextWindowTokens: 128000,
		MaxOutputTokens:     32768, // exactly reasoning.High's floor
		ReasoningEfforts:    []reasoning.Level{reasoning.High},
	}}
	if _, err := normalizeModels(in, 0, "zai"); err != nil {
		t.Fatalf("unexpected error at the exact floor boundary: %v", err)
	}
}

// TestCheckOutputReserveFloorIgnoresModelsWithNoDeclaredEfforts documents that
// the check is scoped to declared reasoning_efforts only: a model with no
// reasoning surface is unaffected by this gate regardless of how low its
// max_output_tokens is set.
func TestCheckOutputReserveFloorIgnoresModelsWithNoDeclaredEfforts(t *testing.T) {
	in := []ModelSpec{{
		Name:                "m",
		ContextWindowTokens: 2048,
		MaxOutputTokens:     100, // far below any reasoning floor
	}}
	if _, err := normalizeModels(in, 0, "zai"); err != nil {
		t.Fatalf("unexpected error for a model with no reasoning_efforts: %v", err)
	}
}
