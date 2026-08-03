package tools

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestSplitTiersNilCoreIsInert(t *testing.T) {
	effective := []string{"read_file", "grep", "fetch_url"}
	got := SplitTiers(effective, nil)
	if !slices.Equal(got.Core, effective) {
		t.Fatalf("core = %v, want %v", got.Core, effective)
	}
	if len(got.Deferred) != 0 {
		t.Fatalf("deferred = %v, want empty", got.Deferred)
	}
}

func TestSplitTiersPreservesEffectiveOrder(t *testing.T) {
	effective := []string{"read_file", "fetch_url", "grep", "search"}
	got := SplitTiers(effective, []string{"grep", "read_file", "not_authorized"})
	if want := []string{"read_file", "grep"}; !slices.Equal(got.Core, want) {
		t.Fatalf("core = %v, want %v", got.Core, want)
	}
	if want := []string{"fetch_url", "search"}; !slices.Equal(got.Deferred, want) {
		t.Fatalf("deferred = %v, want %v", got.Deferred, want)
	}
}

func TestSplitTiersEmptyCoreDefersEverything(t *testing.T) {
	got := SplitTiers([]string{"read_file"}, []string{})
	if len(got.Core) != 0 {
		t.Fatalf("core = %v, want empty", got.Core)
	}
	if want := []string{"read_file"}; !slices.Equal(got.Deferred, want) {
		t.Fatalf("deferred = %v, want %v", got.Deferred, want)
	}
}

func TestMatchDeferredLexicalOverNameAndDescription(t *testing.T) {
	candidates := []TierCandidate{
		{Name: "fetch_url", Description: "Fetch a URL over HTTP"},
		{Name: "search", Description: "Web search the public internet"},
		{Name: "grep", Description: "Search file contents"},
	}
	got := MatchDeferred("WEB", candidates)
	if want := []string{"search"}; !slices.Equal(got, want) {
		t.Fatalf("match(web) = %v, want %v", got, want)
	}
	got = MatchDeferred("search", candidates)
	if want := []string{"search", "grep"}; !slices.Equal(got, want) {
		t.Fatalf("match(search) = %v, want %v (registration order)", got, want)
	}
	if got := MatchDeferred("   ", candidates); got != nil {
		t.Fatalf("empty query matched %v", got)
	}
}

func TestDeferredIndexIsOneLinePerTool(t *testing.T) {
	index := DeferredIndex([]TierCandidate{
		{Name: "fetch_url", Description: "Fetch a URL.\nSupports redirects."},
	})
	if !strings.Contains(index, "- fetch_url: Fetch a URL\n") {
		t.Fatalf("index missing one-liner: %q", index)
	}
	if strings.Contains(index, "redirects") {
		t.Fatalf("index kept the full description: %q", index)
	}
	if DeferredIndex(nil) != "" {
		t.Fatalf("empty candidate list must render nothing")
	}
}

func TestAdmissionDigestSeparatesTiers(t *testing.T) {
	a := AdmissionDigest("reviewer", Tiers{Core: []string{"read_file"}, Deferred: []string{"grep"}})
	b := AdmissionDigest("reviewer", Tiers{Core: []string{"read_file", "grep"}})
	if a == b {
		t.Fatalf("digest collided across a different tier split")
	}
	if AdmissionDigest("other", Tiers{Core: []string{"read_file"}, Deferred: []string{"grep"}}) == a {
		t.Fatalf("digest ignored the agent name")
	}
}

// TestAdmissionDigestFingerprintsTheDeferredTierToo covers the half of the
// split D3 exists for. A tool appearing in or leaving the deferred tier with
// the core tier untouched still changes what an admitted name means, so a
// resumed session must see a different digest and drop its set fail-closed.
func TestAdmissionDigestFingerprintsTheDeferredTierToo(t *testing.T) {
	base := AdmissionDigest("reviewer", Tiers{Core: []string{"read_file"}, Deferred: []string{"grep"}})
	cases := []struct {
		name  string
		tiers Tiers
	}{
		{"a tool joined the deferred tier", Tiers{Core: []string{"read_file"}, Deferred: []string{"grep", "glob"}}},
		{"a tool left the deferred tier", Tiers{Core: []string{"read_file"}, Deferred: nil}},
		{"the deferred tier was renamed", Tiers{Core: []string{"read_file"}, Deferred: []string{"glob"}}},
		{"the deferred tier was reordered", Tiers{Core: []string{"read_file"}, Deferred: []string{"glob", "grep"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := AdmissionDigest("reviewer", tc.tiers); got == base {
				t.Fatalf("digest unchanged after %s: %v", tc.name, tc.tiers)
			}
		})
	}
	if AdmissionDigest("reviewer", Tiers{Core: []string{"read_file"}, Deferred: []string{"grep"}}) != base {
		t.Fatal("digest is not stable for an unchanged split")
	}
}

// stubTool is a minimal registry member for scope-ordering assertions.
type stubTool struct{ name string }

func (s stubTool) Name() string               { return s.name }
func (s stubTool) Description() string        { return s.name + " description" }
func (s stubTool) Parameters() map[string]any { return schemaObject(map[string]any{}, nil) }
func (s stubTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "", nil
}

type stubPrivilegedTool struct{ stubTool }

func (stubPrivilegedTool) Privileged() {}

func registryOf(names ...string) *Registry {
	reg := NewRegistry()
	for _, name := range names {
		reg.Register(stubTool{name: name})
	}
	return reg
}

func names(reg *Registry) []string {
	out := make([]string, 0, len(reg.List()))
	for _, t := range reg.List() {
		out = append(out, t.Name())
	}
	return out
}

func TestScopedRegistryWithTailAppendsAdmittedAfterCore(t *testing.T) {
	base := registryOf("alpha", "bravo", "charlie", "delta")
	core := map[string]struct{}{"alpha": {}, "charlie": {}}
	got := ScopedRegistryWithTail(base, ScopeOptions{Mode: ScopeRoot, Allowlist: core}, []string{"bravo"})
	want := []string{"alpha", "charlie", "bravo"}
	if !slices.Equal(names(got), want) {
		t.Fatalf("order = %v, want %v", names(got), want)
	}
}

func TestScopedRegistryWithTailKeepsCoreBlockStableAcrossAdmissions(t *testing.T) {
	base := registryOf("alpha", "bravo", "charlie", "delta")
	core := map[string]struct{}{"alpha": {}, "charlie": {}}
	first := ScopedRegistryWithTail(base, ScopeOptions{Mode: ScopeRoot, Allowlist: core}, nil)
	second := ScopedRegistryWithTail(base, ScopeOptions{Mode: ScopeRoot, Allowlist: core}, []string{"bravo", "delta"})
	firstSpecs, err := json.Marshal(first.OpenAITools())
	if err != nil {
		t.Fatalf("marshal first: %v", err)
	}
	secondAll := second.OpenAITools()
	secondCore, err := json.Marshal(secondAll[:len(first.List())])
	if err != nil {
		t.Fatalf("marshal second core prefix: %v", err)
	}
	if string(firstSpecs) != string(secondCore) {
		t.Fatalf("core block changed across an admission:\n%s\n%s", firstSpecs, secondCore)
	}
	if want := []string{"alpha", "charlie", "bravo", "delta"}; !slices.Equal(names(second), want) {
		t.Fatalf("order = %v, want %v", names(second), want)
	}
}

func TestScopedRegistryWithTailNeverWidensPastScopeRules(t *testing.T) {
	base := NewRegistry()
	base.Register(stubTool{name: "alpha"})
	base.Register(stubPrivilegedTool{stubTool{name: "privileged"}})
	base.Register(stubTool{name: "dispatch_tasks"})
	got := ScopedRegistryWithTail(base,
		ScopeOptions{Mode: ScopeSpawned, Allowlist: map[string]struct{}{"alpha": {}}},
		[]string{"privileged", "dispatch_tasks", "missing"})
	if want := []string{"alpha"}; !slices.Equal(names(got), want) {
		t.Fatalf("spawned tail admitted %v, want %v", names(got), want)
	}
}

// TestScopedRegistryWithTailNeverReadmitsADeniedNameAtRoot covers the mode the
// spawned test above cannot reach. At root the denylist is not automatically
// fatal - ScopedRegistry keeps a denylisted name the agent allowlist carries -
// so host-mediated admission is the one place an operator guardrail
// (ExtraDenylist, INV-AG-29) could be re-entered by a load_tools call. It must
// not be: the tail is a publication decision, never a grant.
func TestScopedRegistryWithTailNeverReadmitsADeniedNameAtRoot(t *testing.T) {
	base := NewRegistry()
	base.Register(stubTool{name: "alpha"})
	base.Register(stubTool{name: "operator_denied"})
	base.Register(stubTool{name: "dispatch_tasks"})
	got := ScopedRegistryWithTail(base,
		ScopeOptions{
			Mode:          ScopeRoot,
			Allowlist:     map[string]struct{}{"alpha": {}},
			ExtraDenylist: []string{"operator_denied"},
		},
		[]string{"operator_denied", "dispatch_tasks"})
	if want := []string{"alpha"}; !slices.Equal(names(got), want) {
		t.Fatalf("root tail admitted %v, want %v", names(got), want)
	}
}

func TestScopedRegistryWithTailIsIdempotent(t *testing.T) {
	base := registryOf("alpha", "bravo")
	got := ScopedRegistryWithTail(base,
		ScopeOptions{Mode: ScopeRoot, Allowlist: map[string]struct{}{"alpha": {}}},
		[]string{"bravo", "bravo", "alpha"})
	if want := []string{"alpha", "bravo"}; !slices.Equal(names(got), want) {
		t.Fatalf("order = %v, want %v", names(got), want)
	}
}

func TestFirstLineHandlesEmptyDescriptions(t *testing.T) {
	index := DeferredIndex([]TierCandidate{{Name: "bare", Description: "   "}})
	if !strings.Contains(index, "- bare\n") {
		t.Fatalf("a description-less tool must still be listed: %q", index)
	}
	if strings.Contains(index, "bare:") {
		t.Fatalf("an empty description must not render a colon: %q", index)
	}
}

func TestScopedRegistryWithTailOnANilRegistry(t *testing.T) {
	got := ScopedRegistryWithTail(nil, ScopeOptions{Mode: ScopeRoot}, []string{"alpha"})
	if got == nil || len(got.List()) != 0 {
		t.Fatalf("nil source produced %v", got)
	}
}
