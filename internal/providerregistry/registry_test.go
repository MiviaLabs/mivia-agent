package providerregistry

import (
	"sort"
	"strings"
	"testing"
)

func TestLookupAndNamesAreStable(t *testing.T) {
	anthropic, ok := Lookup("anthropic")
	if !ok || anthropic.Name != "anthropic" || anthropic.DefaultModel != "claude-sonnet-5" ||
		anthropic.DefaultURL != "https://api.anthropic.com/v1" || anthropic.DefaultAPIKeyEnv != "ANTHROPIC_API_KEY" {
		t.Fatalf("anthropic descriptor=%+v ok=%v", anthropic, ok)
	}
	d, ok := Lookup("DeepSeek")
	if !ok || d.DefaultModel != "deepseek-v4-flash" || d.DefaultAPIKeyEnv != "DEEPSEEK_API_KEY" {
		t.Fatalf("descriptor=%+v ok=%v", d, ok)
	}
	ollama, ok := Lookup("ollama")
	if !ok || ollama.Name != "ollama" || ollama.DefaultModel != "gpt-oss:120b" ||
		ollama.DefaultURL != "https://ollama.com/v1" || ollama.DefaultAPIKeyEnv != "OLLAMA_API_KEY" {
		t.Fatalf("ollama descriptor=%+v ok=%v", ollama, ok)
	}
	minimax, ok := Lookup("minimax")
	if !ok || minimax.Name != "minimax" || minimax.DefaultModel != "MiniMax-M3" ||
		minimax.DefaultURL != "https://api.minimax.io/v1" || minimax.DefaultAPIKeyEnv != "MINIMAX_API_KEY" {
		t.Fatalf("minimax descriptor=%+v ok=%v", minimax, ok)
	}
	llmproxy, ok := Lookup("llmproxycli")
	if !ok || llmproxy.Name != "llmproxycli" || llmproxy.DefaultModel != "claude-sonnet-5" ||
		llmproxy.DefaultURL != "http://127.0.0.1:8317/v1" || llmproxy.DefaultAPIKeyEnv != "CLIPROXY_API_KEY" {
		t.Fatalf("llmproxycli descriptor=%+v ok=%v", llmproxy, ok)
	}
	names := Names()
	if len(names) != 8 || names[0] != "anthropic" || names[1] != "deepseek" || names[2] != "llmgateway" || names[3] != "llmproxycli" || names[4] != "minimax" || names[5] != "ollama" || names[6] != "openrouter" || names[7] != "zai" {
		t.Fatalf("names=%v", names)
	}
	names[0] = "mutated"
	if next := Names(); next[0] != "anthropic" {
		t.Fatalf("Names returned aliased storage: %v", next)
	}
	zai, ok := Lookup("ZAI")
	if !ok || zai.DefaultModel != "glm-5.2" || zai.DefaultURL != "https://api.z.ai/api/paas/v4" || zai.DefaultAPIKeyEnv != "ZAI_API_KEY" {
		t.Fatalf("zai descriptor=%+v ok=%v", zai, ok)
	}
}

// TestLookupCanonicalizesName pins the DC-11 canonical-form contract. Lookup
// normalizes case and surrounding whitespace before it resolves a name. Any
// spelling of a canonical provider name returns the canonical descriptor.
func TestLookupCanonicalizesName(t *testing.T) {
	canonical := map[string]Descriptor{
		"deepseek": {
			Name: "deepseek", DefaultModel: "deepseek-v4-flash",
			DefaultURL: "https://api.deepseek.com/v1", DefaultAPIKeyEnv: "DEEPSEEK_API_KEY",
		},
		"zai": {
			Name: "zai", DefaultModel: "glm-5.2",
			DefaultURL: "https://api.z.ai/api/paas/v4", DefaultAPIKeyEnv: "ZAI_API_KEY",
		},
	}
	rows := []struct {
		name string
		want Descriptor
	}{
		{"deepseek", canonical["deepseek"]},
		{"DeepSeek", canonical["deepseek"]},
		{" DEEPSEEK ", canonical["deepseek"]},
		{"\tDeepSeek\n", canonical["deepseek"]},
		{"  zai  ", canonical["zai"]},
	}
	for _, row := range rows {
		got, ok := Lookup(row.name)
		if !ok {
			t.Errorf("Lookup(%q) returned ok=false, want canonical descriptor %+v", row.name, row.want)
			continue
		}
		if got != row.want {
			t.Errorf("Lookup(%q) returned %+v, want %+v", row.name, got, row.want)
		}
	}
}

// TestLookupRejectsUnknownEmptyAndOversized pins the negative contract. An
// unknown, empty, whitespace-only, malformed, or oversized name returns
// ok=false and a zero Descriptor. None of the rows may panic.
func TestLookupRejectsUnknownEmptyAndOversized(t *testing.T) {
	rows := []string{
		"",
		"   ",
		"\t\n",
		"gemini",                  // plausible but unsupported: rejected like any other unknown name
		"DEEP SEEK",               // interior space survives trim; not a canonical name
		strings.Repeat("x", 8192), // oversized: no panic, no allocation blow-up
	}
	for _, name := range rows {
		got, ok := Lookup(name)
		if ok {
			t.Errorf("Lookup(%q) returned ok=true with %+v, want ok=false and a zero Descriptor", name, got)
			continue
		}
		if got != (Descriptor{}) {
			t.Errorf("Lookup(%q) returned %+v with ok=false, want a zero Descriptor", name, got)
		}
	}
}

// TestNamesAndLookupAgree pins the Names/Lookup round trip. Names is sorted and
// duplicate-free. Every entry and its upper-case spelling resolve through
// Lookup to the entry's canonical descriptor. The returned slice is a fresh
// copy, never aliased storage.
func TestNamesAndLookupAgree(t *testing.T) {
	names := Names()
	if !sort.StringsAreSorted(names) {
		t.Fatalf("Names() is not sorted: %v", names)
	}
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if seen[name] {
			t.Errorf("Names() contains duplicate %q", name)
		}
		seen[name] = true
		for _, probe := range []string{name, strings.ToUpper(name)} {
			d, ok := Lookup(probe)
			if !ok {
				t.Errorf("Lookup(%q) returned ok=false, want ok=true for a canonical name", probe)
				continue
			}
			if d.Name != name {
				t.Errorf("Lookup(%q) returned Descriptor.Name %q, want %q", probe, d.Name, name)
			}
		}
	}
	names[0] = "mutated"
	next := Names()
	for _, entry := range next {
		if entry == "mutated" {
			t.Fatalf("Names() returned aliased storage; the next call contains the mutation: %v", next)
		}
	}
}
