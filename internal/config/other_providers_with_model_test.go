package config

import (
	"reflect"
	"testing"
)

// otherProvidersCatalog builds a synthetic catalog reproducing the exact
// collision that caused the original user-reported confusion: two providers
// (an OpenAI-compatible proxy and a native one) both declaring a model
// literally named "claude-sonnet-5", plus a third provider with a
// differently-named model and a fourth, unselectable (no API key) provider
// that also happens to declare the colliding name.
func otherProvidersCatalog() []ProviderModelGroup {
	return []ProviderModelGroup{
		{
			Provider:   "llmproxycli",
			Selectable: true,
			Models:     []ModelSpec{{Name: "claude-sonnet-5"}, {Name: "gemini-3.7-flash-high"}},
		},
		{
			Provider:   "anthropic",
			Selectable: true,
			Models:     []ModelSpec{{Name: "claude-sonnet-5"}},
		},
		{
			Provider:   "deepseek",
			Selectable: true,
			Models:     []ModelSpec{{Name: "deepseek-v4-flash"}},
		},
		{
			Provider:       "openrouter",
			Selectable:     false, // no API key configured
			DisabledReason: "credential unavailable",
			Models:         []ModelSpec{{Name: "claude-sonnet-5"}},
		},
	}
}

func TestOtherProvidersWithModel_FindsExactlyOneOtherProvider(t *testing.T) {
	r := &Resolved{}
	r.SetModelCatalogForTest(otherProvidersCatalog())
	// Excluding llmproxycli (the active provider) and deepseek (whose only
	// selectable holder besides the excluded one is anthropic - openrouter
	// is unselectable and must not appear).
	got := r.OtherProvidersWithModel("llmproxycli", "claude-sonnet-5")
	want := []string{"anthropic"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("OtherProvidersWithModel() = %v, want %v", got, want)
	}
}

func TestOtherProvidersWithModel_ExcludesUnselectableProvider(t *testing.T) {
	r := &Resolved{}
	r.SetModelCatalogForTest([]ProviderModelGroup{
		{Provider: "llmproxycli", Selectable: true, Models: []ModelSpec{{Name: "x"}}},
		{Provider: "broken", Selectable: false, Models: []ModelSpec{{Name: "x"}}},
	})
	got := r.OtherProvidersWithModel("llmproxycli", "x")
	if len(got) != 0 {
		t.Fatalf("OtherProvidersWithModel() = %v, want empty - an unselectable provider must never be offered as a switch target", got)
	}
}

func TestOtherProvidersWithModel_NotFoundAnywhere(t *testing.T) {
	r := &Resolved{}
	r.SetModelCatalogForTest(otherProvidersCatalog())
	got := r.OtherProvidersWithModel("llmproxycli", "no-such-model")
	if len(got) != 0 {
		t.Fatalf("OtherProvidersWithModel() = %v, want empty for a name that exists nowhere", got)
	}
}

func TestOtherProvidersWithModel_FindsMultipleOtherProviders(t *testing.T) {
	r := &Resolved{}
	r.SetModelCatalogForTest([]ProviderModelGroup{
		{Provider: "a", Selectable: true, Models: []ModelSpec{{Name: "shared"}}},
		{Provider: "b", Selectable: true, Models: []ModelSpec{{Name: "shared"}}},
		{Provider: "c", Selectable: true, Models: []ModelSpec{{Name: "shared"}}},
	})
	got := r.OtherProvidersWithModel("a", "shared")
	want := []string{"b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("OtherProvidersWithModel() = %v, want %v (both other providers, catalog order)", got, want)
	}
}

// The excluded provider itself must never appear in its own results, even
// though it trivially "has" the model - that's the miss that already
// happened, not a switch target.
func TestOtherProvidersWithModel_ExcludesTheActiveProviderItself(t *testing.T) {
	r := &Resolved{}
	r.SetModelCatalogForTest([]ProviderModelGroup{
		{Provider: "llmproxycli", Selectable: true, Models: []ModelSpec{{Name: "claude-sonnet-5"}}},
	})
	got := r.OtherProvidersWithModel("llmproxycli", "claude-sonnet-5")
	if len(got) != 0 {
		t.Fatalf("OtherProvidersWithModel() = %v, want empty - the excluded provider must not appear in its own results", got)
	}
}

// exclude comparison is case-insensitive (provider names are already
// lowercased by config loading, but the caller-supplied active-provider
// string should not have to match case exactly).
func TestOtherProvidersWithModel_ExcludeIsCaseInsensitive(t *testing.T) {
	r := &Resolved{}
	r.SetModelCatalogForTest([]ProviderModelGroup{
		{Provider: "llmproxycli", Selectable: true, Models: []ModelSpec{{Name: "x"}}},
	})
	got := r.OtherProvidersWithModel("LLMPROXYCLI", "x")
	if len(got) != 0 {
		t.Fatalf("OtherProvidersWithModel() = %v, want empty regardless of exclude's case", got)
	}
}

func TestOtherProvidersWithModel_EmptyNameReturnsNil(t *testing.T) {
	r := &Resolved{}
	r.SetModelCatalogForTest(otherProvidersCatalog())
	if got := r.OtherProvidersWithModel("llmproxycli", "   "); got != nil {
		t.Fatalf("OtherProvidersWithModel(empty name) = %v, want nil", got)
	}
}

func TestOtherProvidersWithModel_NilResolved(t *testing.T) {
	var r *Resolved
	if got := r.OtherProvidersWithModel("x", "y"); got != nil {
		t.Fatalf("OtherProvidersWithModel() on nil Resolved = %v, want nil (must not panic)", got)
	}
}
