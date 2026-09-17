package adapter_test

// runner_model_test.go covers handleModel and SelectModel / SelectModelForProvider
// execution paths: empty-catalog guard, availableModelsByProvider's per-group
// skip/fallback branches, explicit model selection parsing, ModelOwners lookup,
// ambiguous model handling, whitespace provider-model commands, disabled provider
// handling, and SelectModel's discarded-reasoning-override notice.

import (
	"context"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider/reasoning"
	"github.com/MiviaLabs/mivia-agent/internal/tui/adapter"
)

// TestCommandRunner_HandleModel_NoModelsLoaded covers handleModel's
// "no models loaded" guard (runner_model.go:25-26): reached only when
// availableModelsByProvider returns an empty slice, which itself requires
// both an empty catalog AND an empty session CurrentModel() (a non-empty
// CurrentModel falls back to a single flat group instead - see the sibling
// test below).
func TestCommandRunner_HandleModel_NoModelsLoaded(t *testing.T) {
	res := &config.Resolved{}
	sess := chat.NewSession(res, nil)
	runner := adapter.NewCommandRunner(sess, res, nil)

	out := runner.Run(context.Background(), "model", "")
	if out.Err != "no models loaded" {
		t.Fatalf("Run(model, \"\") = %+v, want Err %q", out, "no models loaded")
	}
}

// TestCommandRunner_HandleModel_SkipsUnselectableAndEmptyGroupsThenFallsBack
// covers availableModelsByProvider's two per-group continues (an
// unselectable group at line 43, a selectable-but-modelless group at line
// 50) and the resulting empty-catalog fallback to a single flat group built
// from the session's current model (lines 59-61).
func TestCommandRunner_HandleModel_SkipsUnselectableAndEmptyGroupsThenFallsBack(t *testing.T) {
	res := &config.Resolved{ProviderName: "zai", Model: "current-model"}
	res.SetModelCatalogForTest([]config.ProviderModelGroup{
		{Provider: "unselectable", Selectable: false, Models: []config.ModelSpec{{Name: "x"}}},
		{Provider: "empty", Selectable: true, Models: nil},
	})
	sess := chat.NewSession(res, nil)
	runner := adapter.NewCommandRunner(sess, res, nil)

	out := runner.Run(context.Background(), "model", "")
	if out.Err != "" {
		t.Fatalf("unexpected Err: %s", out.Err)
	}
	if len(out.ModelChoiceGroups) != 1 {
		t.Fatalf("ModelChoiceGroups = %+v, want exactly one fallback group", out.ModelChoiceGroups)
	}
	got := out.ModelChoiceGroups[0]
	if got.Provider != "" || len(got.Models) != 1 || got.Models[0] != "current-model" {
		t.Fatalf("fallback group = %+v, want {Provider:\"\", Models:[current-model]}", got)
	}
}

// TestCommandRunner_SelectModel_ProviderRuntimePrefix covers
// resolveProviderAndModel's 1b branch (runner_model.go:82-88): a
// "provider/model" name whose provider prefix matches a config'd
// ProviderRuntimes key but has no corresponding ModelCatalog group at all -
// so the earlier catalog-prefix loop (section 1) cannot match it, and
// resolution falls through to the runtime map instead.
func TestCommandRunner_SelectModel_ProviderRuntimePrefix(t *testing.T) {
	res := &config.Resolved{
		ProviderName: "ollama",
		Model:        "model-a",
		ProviderRuntimes: map[string]config.ProviderRuntime{
			"ollama": {
				ProviderName: "ollama",
				BaseURL:      "http://127.0.0.1:11434",
				Models:       []config.ModelSpec{{Name: "model-a"}},
			},
			"openrouter": {
				ProviderName: "openrouter",
				APIKey:       "sk-or-v1-test",
				APIKeySet:    true,
				Models:       []config.ModelSpec{{Name: "model-b"}},
			},
		},
	}
	// Deliberately no "openrouter" catalog group: section 1 (catalog prefix
	// match) must fail here so resolution reaches section 1b. Without a
	// catalog entry, configuredProfile has nothing to call the model
	// Selectable under, so the real switch downstream fails - what this test
	// pins is that resolution named the right provider (via 1b) before that
	// failure, which the error message's "(openrouter)" tag proves.
	res.SetModelCatalogForTest([]config.ProviderModelGroup{
		{Provider: "ollama", Selectable: true, Active: true, Models: []config.ModelSpec{{Name: "model-a"}}},
	})
	sess := chat.NewSession(res, nil)
	runner := adapter.NewCommandRunner(sess, res, nil)

	out := runner.SelectModel(context.Background(), "openrouter/model-b")
	if out.Err == "" {
		t.Fatalf("expected a downstream switch error (openrouter has no catalog entry), got success: %+v", out)
	}
	if !strings.Contains(out.Err, `"model-b" (openrouter)`) {
		t.Fatalf("error must name the resolved provider/model pair, got %q", out.Err)
	}
}

// TestCommandRunner_SelectModel_RuntimeNameMatchesViaUnicodeCaseFold covers
// resolveProviderAndModel's 1c runtime-map branch (runner_model.go:98-102),
// which is otherwise shadowed by section 1b's near-identical runtime-prefix
// loop (already covered by TestCommandRunner_SelectModel_ProviderRuntimePrefix
// above): 1b compares via strings.ToLower on both sides, while 1c compares
// the same strings via strings.EqualFold - and those two case-insensitive
// comparisons disagree for U+017F LATIN SMALL LETTER LONG S ("ſ"), which
// strings.ToLower leaves untouched (so 1b's HasPrefix on the lowered name
// never matches "s/") but strings.EqualFold still folds into the same case
// class as ASCII "s" (so 1c's EqualFold against the Cut-out "ſ" segment
// does match). This is a real, reachable divergence between the two
// lookups, not a fabricated one.
func TestCommandRunner_SelectModel_RuntimeNameMatchesViaUnicodeCaseFold(t *testing.T) {
	res := &config.Resolved{
		ProviderName: "other-provider",
		Model:        "model-a",
		ProviderRuntimes: map[string]config.ProviderRuntime{
			"s": {
				ProviderName: "s",
				APIKey:       "sk-s-test",
				APIKeySet:    true,
				Models:       []config.ModelSpec{{Name: "model-x"}},
			},
		},
	}
	// No catalog group named "s" or "ſ": sections 1 and 1c's catalog loop
	// must both fail so resolution can only reach 1c's runtime-map check.
	res.SetModelCatalogForTest([]config.ProviderModelGroup{
		{Provider: "other-provider", Selectable: true, Active: true, Models: []config.ModelSpec{{Name: "model-a"}}},
	})
	sess := chat.NewSession(res, nil)
	runner := adapter.NewCommandRunner(sess, res, nil)

	out := runner.SelectModel(context.Background(), "ſ/model-x")
	if out.Err == "" {
		t.Fatalf("expected a downstream switch error ('s' has no catalog entry), got success: %+v", out)
	}
	if !strings.Contains(out.Err, `"model-x" (s)`) {
		t.Fatalf("error must name the runtime-matched provider \"s\" resolved via Unicode case fold, got %q", out.Err)
	}
}

// TestCommandRunner_SelectModel_SlashNameMatchesCatalogProviderCaseInsensitively
// covers resolveProviderAndModel's 1c catalog-lookup branch
// (runner_model.go:93-97). Section 1's prefix loop builds its prefix from
// the UNLOWERED catalog provider name and compares it against the lowered
// input name, so a catalog provider stored with a capital letter never
// matches there - only the case-insensitive EqualFold search in section 1c
// finds it.
func TestCommandRunner_SelectModel_SlashNameMatchesCatalogProviderCaseInsensitively(t *testing.T) {
	res := &config.Resolved{ProviderName: "other-provider", Model: "model-x"}
	res.SetModelCatalogForTest([]config.ProviderModelGroup{
		{Provider: "Anthropic", Selectable: true, Models: []config.ModelSpec{{Name: "model-x"}}},
	})
	sess := chat.NewSession(res, nil)
	runner := adapter.NewCommandRunner(sess, res, nil)

	out := runner.SelectModel(context.Background(), "anthropic/model-x")
	// No ProviderRuntimes entry for "Anthropic" exists, so the switch itself
	// still fails downstream - what this test pins is that resolution named
	// the right provider before that failure, which only section 1c can do
	// here.
	if out.Err == "" {
		t.Fatalf("expected a downstream switch error (no Anthropic runtime configured), got success: %+v", out)
	}
	if !strings.Contains(out.Err, "Anthropic") {
		t.Fatalf("error must name the resolved provider Anthropic, got %q", out.Err)
	}
}

// TestCommandRunner_SelectModel_ExactNameSearchSkipsUnselectableProvider
// covers the exact-name search loop's Selectable skip (runner_model.go:
// ~122-123): a bare name with no provider prefix or slash matches an
// unselectable catalog group first (in catalog order) and a selectable one
// second - the unselectable group must be skipped so the unique-match count
// still resolves to the one real, selectable provider.
func TestCommandRunner_SelectModel_ExactNameSearchSkipsUnselectableProvider(t *testing.T) {
	res := &config.Resolved{
		ProviderName: "ollama",
		Model:        "model-a",
		ProviderRuntimes: map[string]config.ProviderRuntime{
			"ollama": {
				ProviderName: "ollama",
				BaseURL:      "http://127.0.0.1:11434",
				Models:       []config.ModelSpec{{Name: "model-a"}},
			},
			"llmgateway": {
				ProviderName: "llmgateway",
				APIKey:       "sk-llmgateway-test",
				APIKeySet:    true,
				Models:       []config.ModelSpec{{Name: "target-model"}},
			},
		},
	}
	res.SetModelCatalogForTest([]config.ProviderModelGroup{
		{Provider: "ollama", Selectable: true, Active: true, Models: []config.ModelSpec{{Name: "model-a"}}},
		// Catalog-order-first, but unselectable: must be skipped by the
		// exact-name search rather than counted as a match.
		{Provider: "unselectable-provider", Selectable: false, Models: []config.ModelSpec{{Name: "target-model"}}},
		{Provider: "llmgateway", Selectable: true, Models: []config.ModelSpec{{Name: "target-model"}}},
	})
	sess := chat.NewSession(res, nil)
	runner := adapter.NewCommandRunner(sess, res, nil)

	// Bare name, no "/" and no explicit provider: only the exact-name search
	// (part 2) can resolve this.
	out := runner.SelectModel(context.Background(), "target-model")
	if out.Err != "" {
		t.Fatalf("SelectModel error: %v", out.Err)
	}
	if got := sess.CurrentSelection().ProviderName; got != "llmgateway" {
		t.Fatalf("provider = %q, want llmgateway (the sole selectable match)", got)
	}
}

// TestCommandRunner_SelectModel_DoesNotLeakIntoNewSessionsDefault covers a
// cross-session isolation bug: SelectModel used to write the resolved
// provider/model directly onto the *config.Resolved it was given
// (r.res.ProviderName = providerName; r.res.Model = modelName). NewCommandRunner
// hands that SAME *config.Resolved pointer to its SessionPool
// (session_pool.go's NewSessionPool), and chat.NewSession seeds a brand-new
// session's initial binding straight from res.ProviderName/res.Model
// (binding.go's NewSession). So switching THIS session's model - a
// per-session action the user reaches via /model - silently changed the
// workspace's configured default for every OTHER session the pool creates
// afterward (a fresh tab via /new, or a new worktree), leaking one tab's
// choice into tabs that never asked for it.
func TestCommandRunner_SelectModel_DoesNotLeakIntoNewSessionsDefault(t *testing.T) {
	res := &config.Resolved{
		ProviderName: "ollama",
		Model:        "model-a",
		ProviderRuntimes: map[string]config.ProviderRuntime{
			"ollama": {
				ProviderName: "ollama",
				BaseURL:      "http://127.0.0.1:11434",
				Models:       []config.ModelSpec{{Name: "model-a"}},
			},
			"llmgateway": {
				ProviderName: "llmgateway",
				APIKey:       "sk-llmgateway-test",
				APIKeySet:    true,
				Models:       []config.ModelSpec{{Name: "target-model"}},
			},
		},
	}
	res.SetModelCatalogForTest([]config.ProviderModelGroup{
		{Provider: "ollama", Selectable: true, Active: true, Models: []config.ModelSpec{{Name: "model-a"}}},
		{Provider: "llmgateway", Selectable: true, Models: []config.ModelSpec{{Name: "target-model"}}},
	})
	sess := chat.NewSession(res, nil)
	runner := adapter.NewCommandRunner(sess, res, nil)

	out := runner.SelectModel(context.Background(), "target-model")
	if out.Err != "" {
		t.Fatalf("SelectModel error: %v", out.Err)
	}
	if got := sess.CurrentSelection(); got.ProviderName != "llmgateway" || got.Model != "target-model" {
		t.Fatalf("session did not switch to the requested model: %+v", got)
	}

	conv, err := runner.Pool().CreateFresh()
	if err != nil {
		t.Fatalf("CreateFresh: %v", err)
	}
	newSess := runner.Pool().Session(conv.ID())
	if newSess == nil {
		t.Fatal("CreateFresh did not register the new session in the pool")
	}
	if got := newSess.CurrentSelection(); got.ProviderName != "ollama" || got.Model != "model-a" {
		t.Errorf("new session started on %+v, want the workspace default {ollama model-a} - session A's /model switch leaked into it", got)
	}
}

// TestCommandRunner_SelectModel_SwitchFailureNamesTheSingleOtherProvider
// covers SelectModel's len(others)==1 hint (runner_model.go:~155-156): the
// resolved provider exists in the catalog but is not Selectable, so the
// switch itself fails, and exactly one OTHER Selectable provider carries the
// same model name - the error must name it as the fix.
func TestCommandRunner_SelectModel_SwitchFailureNamesTheSingleOtherProvider(t *testing.T) {
	res := &config.Resolved{ProviderName: "other-provider", Model: "shared-model"}
	res.SetModelCatalogForTest([]config.ProviderModelGroup{
		{Provider: "target-provider", Selectable: false, Models: []config.ModelSpec{{Name: "shared-model"}}},
		{Provider: "other-provider", Selectable: true, Active: true, Models: []config.ModelSpec{{Name: "shared-model"}}},
	})
	sess := chat.NewSession(res, nil)
	runner := adapter.NewCommandRunner(sess, res, nil)

	out := runner.SelectModel(context.Background(), "target-provider/shared-model")
	if out.Err == "" {
		t.Fatalf("expected an error switching to the unselectable target-provider, got success: %+v", out)
	}
	if !strings.Contains(out.Err, "found under provider other-provider") {
		t.Fatalf("error must name the single other provider, got %q", out.Err)
	}
	if !strings.Contains(out.Err, "/model other-provider shared-model") {
		t.Fatalf("error must give the exact disambiguating command, got %q", out.Err)
	}
}

// TestCommandRunner_SelectModel_DiscardsReasoningOverrideOnPlainRename
// covers SelectModel's discarded-override notice branch (runner_model.go:
// 166-167): switching from a thinking model with a chosen /effort override
// to a plain model with no reasoning surface at all discards the choice, and
// the notice must say so.
//
// NewCommandRunner always wires a real sessionBindingFactory (via its
// SessionPool - see session_pool.go's NewSessionPool), so - unlike
// cliagents's own SwitchModelCommand unit tests - the switch here always
// runs through real provider construction and needs a genuinely configured,
// Selectable catalog entry with a credential the fake-key construction path
// accepts, matching the fixture shape TestCommandRunner_ModelSwitching uses.
func TestCommandRunner_SelectModel_DiscardsReasoningOverrideOnPlainRename(t *testing.T) {
	const thinker = "thinker-model"
	const plain = "plain-model"
	res := &config.Resolved{
		ProviderName: "zai",
		Model:        thinker,
		ModelProfiles: []config.ModelSpec{
			{
				Name: thinker, ContextWindowTokens: 200000,
				ReasoningEfforts: []reasoning.Level{reasoning.Low, reasoning.Medium, reasoning.High},
				Reasoning:        reasoning.High,
				ReasoningDialect: reasoning.DialectThinkingEffort,
			},
			{Name: plain, ContextWindowTokens: 200000},
		},
		ProviderRuntimes: map[string]config.ProviderRuntime{
			"zai": {
				ProviderName: "zai",
				APIKey:       "test-key",
				APIKeySet:    true,
				Models: []config.ModelSpec{
					{
						Name: thinker, ContextWindowTokens: 200000,
						ReasoningEfforts: []reasoning.Level{reasoning.Low, reasoning.Medium, reasoning.High},
						Reasoning:        reasoning.High,
						ReasoningDialect: reasoning.DialectThinkingEffort,
					},
					{Name: plain, ContextWindowTokens: 200000},
				},
			},
		},
	}
	res.SetModelCatalogForTest([]config.ProviderModelGroup{
		{
			Provider:   "zai",
			Selectable: true,
			Active:     true,
			Models: []config.ModelSpec{
				{
					Name: thinker, ContextWindowTokens: 200000,
					ReasoningEfforts: []reasoning.Level{reasoning.Low, reasoning.Medium, reasoning.High},
					Reasoning:        reasoning.High,
					ReasoningDialect: reasoning.DialectThinkingEffort,
				},
				{Name: plain, ContextWindowTokens: 200000},
			},
		},
	})
	sess := chat.NewSession(res, &nullCompleter{})
	if err := sess.SetReasoningEffort(reasoning.High); err != nil {
		t.Fatalf("SetReasoningEffort: %v", err)
	}
	runner := adapter.NewCommandRunner(sess, res, nil)

	out := runner.SelectModel(context.Background(), plain)
	if out.Err != "" {
		t.Fatalf("SelectModel error: %v", out.Err)
	}
	if !strings.Contains(out.Notice, "Reasoning effort override") || !strings.Contains(out.Notice, "discarded") {
		t.Fatalf("expected a discarded-override notice, got %q", out.Notice)
	}
	if !strings.Contains(out.Notice, string(reasoning.High)) {
		t.Fatalf("notice must name the discarded level %q, got %q", reasoning.High, out.Notice)
	}
}

// TestCommandRunner_SelectModel_UniqueExactCollisionFullIDWinsOverProviderPrefix
// covers priority ordering when an exact model ID matches a configured provider
// prefix (e.g. openrouter/deepseek-v4.1-flash under llmgateway).
// Priority tradeoff: step 1 exact whole-ID catalog matching intentionally takes
// priority over step 2 provider-prefix parsing. When a model ID contains a slash
// matching a selectable provider name ("openrouter/...") but is explicitly
// declared under another provider (llmgateway), the exact catalog match wins
// rather than stripping the prefix.
func TestCommandRunner_SelectModel_UniqueExactCollisionFullIDWinsOverProviderPrefix(t *testing.T) {
	res := &config.Resolved{
		ProviderName: "ollama",
		Model:        "model-a",
		ProviderRuntimes: map[string]config.ProviderRuntime{
			"ollama": {
				ProviderName: "ollama",
				BaseURL:      "http://127.0.0.1:11434",
				Models:       []config.ModelSpec{{Name: "model-a"}},
			},
			"llmgateway": {
				ProviderName: "llmgateway",
				APIKey:       "sk-llm-test",
				APIKeySet:    true,
				Models:       []config.ModelSpec{{Name: "openrouter/deepseek-v4.1-flash"}},
			},
			"openrouter": {
				ProviderName: "openrouter",
				APIKey:       "sk-or-test",
				APIKeySet:    true,
				Models:       []config.ModelSpec{{Name: "deepseek-v4.1-flash"}},
			},
		},
	}
	res.SetModelCatalogForTest([]config.ProviderModelGroup{
		{Provider: "ollama", Selectable: true, Active: true, Models: []config.ModelSpec{{Name: "model-a"}}},
		{Provider: "llmgateway", Selectable: true, Models: []config.ModelSpec{{Name: "openrouter/deepseek-v4.1-flash"}}},
		{Provider: "openrouter", Selectable: true, Models: []config.ModelSpec{{Name: "deepseek-v4.1-flash"}}},
	})
	sess := chat.NewSession(res, &nullCompleter{})
	runner := adapter.NewCommandRunner(sess, res, nil)

	out := runner.SelectModel(context.Background(), "openrouter/deepseek-v4.1-flash")
	if out.Err != "" {
		t.Fatalf("SelectModel error: %v", out.Err)
	}
	got := sess.CurrentSelection()
	if got.ProviderName != "llmgateway" {
		t.Fatalf("provider = %q, want llmgateway (exact model ID should win over openrouter prefix)", got.ProviderName)
	}
	if got.Model != "openrouter/deepseek-v4.1-flash" {
		t.Fatalf("model = %q, want openrouter/deepseek-v4.1-flash", got.Model)
	}
}

// TestCommandRunner_SelectModel_LiteralNoncollisionFullIDSelectsGateway covers
// resolving a literal non-colliding slash-containing model ID configured under
// llmgateway to llmgateway with its full name intact.
func TestCommandRunner_SelectModel_LiteralNoncollisionFullIDSelectsGateway(t *testing.T) {
	const fullID = "consensusprotocol/deepseek-v4.1-flash"
	res := &config.Resolved{
		ProviderName: "ollama",
		Model:        "model-a",
		ProviderRuntimes: map[string]config.ProviderRuntime{
			"ollama": {
				ProviderName: "ollama",
				BaseURL:      "http://127.0.0.1:11434",
				Models:       []config.ModelSpec{{Name: "model-a"}},
			},
			"llmgateway": {
				ProviderName: "llmgateway",
				APIKey:       "sk-llm-test",
				APIKeySet:    true,
				Models:       []config.ModelSpec{{Name: fullID}},
			},
		},
	}
	res.SetModelCatalogForTest([]config.ProviderModelGroup{
		{Provider: "ollama", Selectable: true, Active: true, Models: []config.ModelSpec{{Name: "model-a"}}},
		{Provider: "llmgateway", Selectable: true, Models: []config.ModelSpec{{Name: fullID}}},
	})
	sess := chat.NewSession(res, &nullCompleter{})
	runner := adapter.NewCommandRunner(sess, res, nil)

	out := runner.SelectModel(context.Background(), fullID)
	if out.Err != "" {
		t.Fatalf("SelectModel error: %v", out.Err)
	}
	got := sess.CurrentSelection()
	if got.ProviderName != "llmgateway" || got.Model != fullID {
		t.Fatalf("selection = %+v, want {llmgateway %s}", got, fullID)
	}
}

// TestCommandRunner_SelectModel_UnselectableExactFullIDOwnerSkippedForSelectableOwner
// verifies that an unselectable catalog group defining the exact full slash ID
// is skipped during unique exact-match resolution in favor of the selectable owner.
func TestCommandRunner_SelectModel_UnselectableExactFullIDOwnerSkippedForSelectableOwner(t *testing.T) {
	const modelID = "customprefix/deepseek-v4.1-flash"
	res := &config.Resolved{
		ProviderName: "ollama",
		Model:        "model-a",
		ProviderRuntimes: map[string]config.ProviderRuntime{
			"ollama": {
				ProviderName: "ollama",
				BaseURL:      "http://127.0.0.1:11434",
				Models:       []config.ModelSpec{{Name: "model-a"}},
			},
			"llmgateway": {
				ProviderName: "llmgateway",
				APIKey:       "sk-llm-test",
				APIKeySet:    true,
				Models:       []config.ModelSpec{{Name: modelID}},
			},
		},
	}
	res.SetModelCatalogForTest([]config.ProviderModelGroup{
		{Provider: "ollama", Selectable: true, Active: true, Models: []config.ModelSpec{{Name: "model-a"}}},
		{Provider: "disabled-gw", Selectable: false, Models: []config.ModelSpec{{Name: modelID}}},
		{Provider: "llmgateway", Selectable: true, Models: []config.ModelSpec{{Name: modelID}}},
	})
	sess := chat.NewSession(res, &nullCompleter{})
	runner := adapter.NewCommandRunner(sess, res, nil)

	out := runner.SelectModel(context.Background(), modelID)
	if out.Err != "" {
		t.Fatalf("SelectModel error: %v", out.Err)
	}
	got := sess.CurrentSelection()
	if got.ProviderName != "llmgateway" || got.Model != modelID {
		t.Fatalf("selection = %+v, want {llmgateway %s}", got, modelID)
	}
}

// TestCommandRunner_SelectModel_CaseSensitiveFullIDMatch verifies that
// whole-ID resolution is strictly case-sensitive.
func TestCommandRunner_SelectModel_CaseSensitiveFullIDMatch(t *testing.T) {
	const exactModel = "vendor/DeepSeek-V4.1-Flash"
	res := &config.Resolved{
		ProviderName: "ollama",
		Model:        "model-a",
		ProviderRuntimes: map[string]config.ProviderRuntime{
			"ollama": {
				ProviderName: "ollama",
				BaseURL:      "http://127.0.0.1:11434",
				Models:       []config.ModelSpec{{Name: "model-a"}},
			},
			"llmgateway": {
				ProviderName: "llmgateway",
				APIKey:       "sk-llm-test",
				APIKeySet:    true,
				Models:       []config.ModelSpec{{Name: exactModel}},
			},
		},
	}
	res.SetModelCatalogForTest([]config.ProviderModelGroup{
		{Provider: "ollama", Selectable: true, Active: true, Models: []config.ModelSpec{{Name: "model-a"}}},
		{Provider: "llmgateway", Selectable: true, Models: []config.ModelSpec{{Name: exactModel}}},
	})
	sess := chat.NewSession(res, &nullCompleter{})
	runner := adapter.NewCommandRunner(sess, res, nil)

	// Casing mismatch: exact-name step 1 does not match, vendor/ prefix step 2 has no vendor provider.
	mismatched := "vendor/deepseek-v4.1-flash"
	out := runner.SelectModel(context.Background(), mismatched)
	if out.Err == "" {
		t.Fatalf("SelectModel(%q) should fail when casing does not match exact model, got success: %+v", mismatched, out)
	}

	// Exact case match succeeds and selects llmgateway.
	out = runner.SelectModel(context.Background(), exactModel)
	if out.Err != "" {
		t.Fatalf("SelectModel(%q) error: %v", exactModel, out.Err)
	}
	if got := sess.CurrentSelection(); got.ProviderName != "llmgateway" || got.Model != exactModel {
		t.Fatalf("selection = %+v, want {llmgateway %s}", got, exactModel)
	}
}

// TestCommandRunner_SelectModel_NoExactFullIDRetainsProviderPrefixBehavior
// ensures that when no provider has an exact whole-ID match, standard prefix
// stripping behavior remains active.
func TestCommandRunner_SelectModel_NoExactFullIDRetainsProviderPrefixBehavior(t *testing.T) {
	res := &config.Resolved{
		ProviderName: "ollama",
		Model:        "model-a",
		ProviderRuntimes: map[string]config.ProviderRuntime{
			"ollama": {
				ProviderName: "ollama",
				BaseURL:      "http://127.0.0.1:11434",
				Models:       []config.ModelSpec{{Name: "model-a"}},
			},
			"openrouter": {
				ProviderName: "openrouter",
				APIKey:       "sk-or-test",
				APIKeySet:    true,
				Models:       []config.ModelSpec{{Name: "deepseek-v4.1-flash"}},
			},
		},
	}
	res.SetModelCatalogForTest([]config.ProviderModelGroup{
		{Provider: "ollama", Selectable: true, Active: true, Models: []config.ModelSpec{{Name: "model-a"}}},
		{Provider: "openrouter", Selectable: true, Models: []config.ModelSpec{{Name: "deepseek-v4.1-flash"}}},
	})
	sess := chat.NewSession(res, &nullCompleter{})
	runner := adapter.NewCommandRunner(sess, res, nil)

	out := runner.SelectModel(context.Background(), "openrouter/deepseek-v4.1-flash")
	if out.Err != "" {
		t.Fatalf("SelectModel error: %v", out.Err)
	}
	got := sess.CurrentSelection()
	if got.ProviderName != "openrouter" || got.Model != "deepseek-v4.1-flash" {
		t.Fatalf("selection = %+v, want {openrouter deepseek-v4.1-flash}", got)
	}
}

// TestCommandRunner_SelectModel_AmbiguousFullIDRefusesCatalogOrderAndPrefixDisambiguates
// proves that an exact full slash ID matching two selectable catalog groups does
// not silently pick the first match by catalog order; and that an explicit provider
// prefix like zai/shared-model correctly parses to provider zai when whole-ID is ambiguous.
func TestCommandRunner_SelectModel_AmbiguousFullIDRefusesCatalogOrderAndPrefixDisambiguates(t *testing.T) {
	const sharedModel = "zai/shared-model"
	res := &config.Resolved{
		ProviderName: "ollama",
		Model:        "model-a",
		ProviderRuntimes: map[string]config.ProviderRuntime{
			"ollama": {
				ProviderName: "ollama",
				BaseURL:      "http://127.0.0.1:11434",
				Models:       []config.ModelSpec{{Name: "model-a"}},
			},
			"gw1": {
				ProviderName: "gw1",
				APIKey:       "sk-gw1-test",
				APIKeySet:    true,
				Models:       []config.ModelSpec{{Name: sharedModel}, {Name: "customgw/shared-model"}},
			},
			"gw2": {
				ProviderName: "gw2",
				APIKey:       "sk-gw2-test",
				APIKeySet:    true,
				Models:       []config.ModelSpec{{Name: sharedModel}, {Name: "customgw/shared-model"}},
			},
			"zai": {
				ProviderName: "zai",
				APIKey:       "sk-zai-test",
				APIKeySet:    true,
				Models:       []config.ModelSpec{{Name: "shared-model"}},
			},
		},
	}
	res.SetModelCatalogForTest([]config.ProviderModelGroup{
		{Provider: "ollama", Selectable: true, Active: true, Models: []config.ModelSpec{{Name: "model-a"}}},
		{Provider: "gw1", Selectable: true, Models: []config.ModelSpec{{Name: sharedModel}, {Name: "customgw/shared-model"}}},
		{Provider: "gw2", Selectable: true, Models: []config.ModelSpec{{Name: sharedModel}, {Name: "customgw/shared-model"}}},
		{Provider: "zai", Selectable: true, Models: []config.ModelSpec{{Name: "shared-model"}}},
	})
	sess := chat.NewSession(res, &nullCompleter{})
	runner := adapter.NewCommandRunner(sess, res, nil)

	// 1. Ambiguous whole-ID without provider prefix match: customgw/shared-model matches gw1 and gw2.
	// It must NOT pick gw1 just because gw1 is earlier in catalog order; it must fail with ambiguity.
	out := runner.SelectModel(context.Background(), "customgw/shared-model")
	if out.Err == "" {
		t.Fatalf("SelectModel(ambiguous whole ID) must fail, got success: %+v", out)
	}
	if sess.CurrentSelection().ProviderName == "gw1" || sess.CurrentSelection().ProviderName == "gw2" {
		t.Fatalf("provider switched to %q on ambiguous name, want refusal", sess.CurrentSelection().ProviderName)
	}

	// 2. Whole-ID match is ambiguous across gw1 and gw2, but zai/ is an explicit provider prefix.
	// It must fall through step 1 (matches == 2) to step 2 and parse to provider "zai", model "shared-model".
	out = runner.SelectModel(context.Background(), sharedModel)
	if out.Err != "" {
		t.Fatalf("SelectModel(%q) error: %v", sharedModel, out.Err)
	}
	got := sess.CurrentSelection()
	if got.ProviderName != "zai" || got.Model != "shared-model" {
		t.Fatalf("selection = %+v, want {zai shared-model}", got)
	}
}

// TestCommandRunner_SelectModel_BareCurrentProviderIsOneOfMultipleExactError
// proves that when a bare model name is ambiguous across multiple providers,
// and the session's active provider is one of those owners, SelectModel does NOT
// silently fall back to the current provider. It must return the exact ambiguous
// error and leave the session unchanged.
func TestCommandRunner_SelectModel_BareCurrentProviderIsOneOfMultipleExactError(t *testing.T) {
	res := &config.Resolved{
		ProviderName: "ollama",
		Model:        "initial-model",
		ProviderRuntimes: map[string]config.ProviderRuntime{
			"ollama": {
				ProviderName: "ollama",
				BaseURL:      "http://127.0.0.1:11434",
				Models:       []config.ModelSpec{{Name: "initial-model"}, {Name: "shared-model"}},
			},
			"llmgateway": {
				ProviderName: "llmgateway",
				APIKey:       "sk-gw-test",
				APIKeySet:    true,
				Models:       []config.ModelSpec{{Name: "shared-model"}},
			},
		},
	}
	res.SetModelCatalogForTest([]config.ProviderModelGroup{
		{Provider: "ollama", Selectable: true, Active: true, Models: []config.ModelSpec{{Name: "initial-model"}, {Name: "shared-model"}}},
		{Provider: "llmgateway", Selectable: true, Models: []config.ModelSpec{{Name: "shared-model"}}},
	})
	sess := chat.NewSession(res, &nullCompleter{})
	runner := adapter.NewCommandRunner(sess, res, nil)

	out := runner.SelectModel(context.Background(), "shared-model")
	wantMsg := `model "shared-model" is ambiguous across providers: ollama, llmgateway; run /model <provider> shared-model to switch`
	if out.Err != wantMsg {
		t.Fatalf("SelectModel(shared-model) err = %q, want exact error %q", out.Err, wantMsg)
	}
	got := sess.CurrentSelection()
	if got.ProviderName != "ollama" || got.Model != "initial-model" {
		t.Fatalf("session modified unexpectedly on ambiguous error: %+v", got)
	}
}

// TestCommandRunner_SelectModel_UniqueOwnerDirectSwitch tests that a model
// with a single owner in the catalog switches directly to that owner without legacy parsing.
func TestCommandRunner_SelectModel_UniqueOwnerDirectSwitch(t *testing.T) {
	res := &config.Resolved{
		ProviderName: "ollama",
		Model:        "model-a",
		ProviderRuntimes: map[string]config.ProviderRuntime{
			"ollama": {
				ProviderName: "ollama",
				BaseURL:      "http://127.0.0.1:11434",
				Models:       []config.ModelSpec{{Name: "model-a"}},
			},
			"llmgateway": {
				ProviderName: "llmgateway",
				APIKey:       "sk-gw-test",
				APIKeySet:    true,
				Models:       []config.ModelSpec{{Name: "unique-model"}},
			},
		},
	}
	res.SetModelCatalogForTest([]config.ProviderModelGroup{
		{Provider: "ollama", Selectable: true, Active: true, Models: []config.ModelSpec{{Name: "model-a"}}},
		{Provider: "llmgateway", Selectable: true, Models: []config.ModelSpec{{Name: "unique-model"}}},
	})
	sess := chat.NewSession(res, &nullCompleter{})
	runner := adapter.NewCommandRunner(sess, res, nil)

	out := runner.SelectModel(context.Background(), "unique-model")
	if out.Err != "" {
		t.Fatalf("SelectModel(unique-model) unexpected err: %v", out.Err)
	}
	got := sess.CurrentSelection()
	if got.ProviderName != "llmgateway" || got.Model != "unique-model" {
		t.Fatalf("selection = %+v, want {llmgateway unique-model}", got)
	}
}

// TestCommandRunner_SelectModel_NoOwnerFallsBackToCurrentProvider verifies that
// when a model is not owned by any selectable provider in the catalog and does not
// match explicit provider prefixes, it attempts the switch under the current provider.
func TestCommandRunner_SelectModel_NoOwnerFallsBackToCurrentProvider(t *testing.T) {
	res := &config.Resolved{
		ProviderName: "ollama",
		Model:        "model-a",
		Models:       []string{"model-a", "custom-unlisted"},
		ProviderRuntimes: map[string]config.ProviderRuntime{
			"ollama": {
				ProviderName: "ollama",
				BaseURL:      "http://127.0.0.1:11434",
				Models:       []config.ModelSpec{{Name: "model-a"}, {Name: "custom-unlisted"}},
			},
		},
	}
	sess := chat.NewSession(res, &nullCompleter{})
	runner := adapter.NewCommandRunner(sess, res, nil)

	out := runner.SelectModel(context.Background(), "custom-unlisted")
	if out.Err != "" {
		t.Fatalf("SelectModel(custom-unlisted) err: %v", out.Err)
	}
	got := sess.CurrentSelection()
	if got.ProviderName != "ollama" || got.Model != "custom-unlisted" {
		t.Fatalf("selection = %+v, want {ollama custom-unlisted}", got)
	}
}

// TestCommandRunner_HandleModel_ExplicitWhitespaceProviderModel tests "/model <provider> <model>"
// syntax for switching directly via SelectModelForProvider.
func TestCommandRunner_HandleModel_ExplicitWhitespaceProviderModel(t *testing.T) {
	res := &config.Resolved{
		ProviderName: "ollama",
		Model:        "llama3",
		ProviderRuntimes: map[string]config.ProviderRuntime{
			"ollama": {
				ProviderName: "ollama",
				BaseURL:      "http://127.0.0.1:11434",
				Models:       []config.ModelSpec{{Name: "llama3"}},
			},
			"llmgateway": {
				ProviderName: "llmgateway",
				APIKey:       "sk-gw-test",
				APIKeySet:    true,
				Models:       []config.ModelSpec{{Name: "deepseek-v4.1-flash"}},
			},
		},
	}
	res.SetModelCatalogForTest([]config.ProviderModelGroup{
		{Provider: "ollama", Selectable: true, Active: true, Models: []config.ModelSpec{{Name: "llama3"}}},
		{Provider: "llmgateway", Selectable: true, Models: []config.ModelSpec{{Name: "deepseek-v4.1-flash"}}},
	})
	sess := chat.NewSession(res, &nullCompleter{})
	runner := adapter.NewCommandRunner(sess, res, nil)

	out := runner.Run(context.Background(), "model", "llmgateway deepseek-v4.1-flash")
	if out.Err != "" {
		t.Fatalf("Run(model, \"llmgateway deepseek-v4.1-flash\") error: %v", out.Err)
	}
	got := sess.CurrentSelection()
	if got.ProviderName != "llmgateway" || got.Model != "deepseek-v4.1-flash" {
		t.Fatalf("selection = %+v, want {llmgateway deepseek-v4.1-flash}", got)
	}
}

// TestCommandRunner_HandleModel_ExplicitDisabledProviderFailsClosed tests that
// specifying an unselectable or disabled provider fails closed downstream.
func TestCommandRunner_HandleModel_ExplicitDisabledProviderFailsClosed(t *testing.T) {
	res := &config.Resolved{
		ProviderName: "ollama",
		Model:        "llama3",
		ProviderRuntimes: map[string]config.ProviderRuntime{
			"ollama": {
				ProviderName: "ollama",
				BaseURL:      "http://127.0.0.1:11434",
				Models:       []config.ModelSpec{{Name: "llama3"}},
			},
		},
	}
	res.SetModelCatalogForTest([]config.ProviderModelGroup{
		{Provider: "ollama", Selectable: true, Active: true, Models: []config.ModelSpec{{Name: "llama3"}}},
		{Provider: "disabled-prov", Selectable: false, DisabledReason: "no key", Models: []config.ModelSpec{{Name: "some-model"}}},
	})
	sess := chat.NewSession(res, &nullCompleter{})
	runner := adapter.NewCommandRunner(sess, res, nil)

	out := runner.Run(context.Background(), "model", "disabled-prov some-model")
	if out.Err == "" {
		t.Fatal("Run(model, disabled-prov some-model) expected error, got success")
	}
	if !strings.Contains(out.Err, "failed to switch model") && !strings.Contains(out.Err, "disabled-prov") {
		t.Fatalf("unexpected error message: %q", out.Err)
	}
}

// TestCommandRunner_HandleModel_ExplicitNonOwnerProviderFailsClosed tests that
// specifying a provider that does not own the requested model fails closed downstream.
func TestCommandRunner_HandleModel_ExplicitNonOwnerProviderFailsClosed(t *testing.T) {
	res := &config.Resolved{
		ProviderName: "ollama",
		Model:        "llama3",
		ProviderRuntimes: map[string]config.ProviderRuntime{
			"ollama": {
				ProviderName: "ollama",
				BaseURL:      "http://127.0.0.1:11434",
				Models:       []config.ModelSpec{{Name: "llama3"}},
			},
			"openai": {
				ProviderName: "openai",
				APIKey:       "sk-test",
				APIKeySet:    true,
				Models:       []config.ModelSpec{{Name: "gpt-4o"}},
			},
		},
	}
	res.SetModelCatalogForTest([]config.ProviderModelGroup{
		{Provider: "ollama", Selectable: true, Active: true, Models: []config.ModelSpec{{Name: "llama3"}}},
		{Provider: "openai", Selectable: true, Models: []config.ModelSpec{{Name: "gpt-4o"}}},
	})
	sess := chat.NewSession(res, &nullCompleter{})
	runner := adapter.NewCommandRunner(sess, res, nil)

	out := runner.Run(context.Background(), "model", "openai non-existent-model")
	if out.Err == "" {
		t.Fatal("Run(model, openai non-existent-model) expected error, got success")
	}
}

// TestCommandRunner_HandleModel_ProviderArgMatchesRuntimeMap covers
// findProvider's ProviderRuntimes branch (runner_model.go:58-63): a
// "<provider> <model>" handleModel argument whose provider token matches no
// ModelCatalog provider but does match a configured ProviderRuntimes key
// (case-folded). The catalog loop must miss first, so resolution reaches the
// runtime map and dispatches SelectModelForProvider with the runtime's
// canonical name.
func TestCommandRunner_HandleModel_ProviderArgMatchesRuntimeMap(t *testing.T) {
	res := &config.Resolved{
		ProviderName: "ollama",
		Model:        "model-a",
		ProviderRuntimes: map[string]config.ProviderRuntime{
			"ollama": {
				ProviderName: "ollama",
				BaseURL:      "http://127.0.0.1:11434",
				Models:       []config.ModelSpec{{Name: "model-a"}},
			},
			"openrouter": {
				ProviderName: "openrouter",
				APIKey:       "sk-or-v1-test",
				APIKeySet:    true,
				Models:       []config.ModelSpec{{Name: "model-b"}},
			},
		},
	}
	// No "openrouter" catalog group: the catalog loop in findProvider must
	// miss so the runtime-map branch is what matches. Without a catalog
	// entry the downstream switch fails; what this test pins is that
	// resolution reached SelectModelForProvider under the runtime's name,
	// which the error message's "(openrouter)" tag proves.
	res.SetModelCatalogForTest([]config.ProviderModelGroup{
		{Provider: "ollama", Selectable: true, Active: true, Models: []config.ModelSpec{{Name: "model-a"}}},
	})
	sess := chat.NewSession(res, &nullCompleter{})
	runner := adapter.NewCommandRunner(sess, res, nil)

	out := runner.Run(context.Background(), "model", "OpenRouter model-b")
	if out.Err == "" {
		t.Fatal("Run(model, OpenRouter model-b) expected the downstream switch error, got success")
	}
	if !strings.Contains(out.Err, `"model-b" (openrouter)`) {
		t.Fatalf("error must name the resolved provider/model pair, got %q", out.Err)
	}
}

// TestCommandRunner_HandleModel_ProviderArgMatchesNothing covers
// findProvider's final miss return (runner_model.go:65-66): a "<provider>
// <model>" argument whose provider token matches neither a catalog provider
// nor a ProviderRuntimes key, so handleModel abandons the two-token form and
// treats the whole argument as a plain model name via SelectModel.
func TestCommandRunner_HandleModel_ProviderArgMatchesNothing(t *testing.T) {
	res := &config.Resolved{
		ProviderName: "ollama",
		Model:        "model-a",
		ProviderRuntimes: map[string]config.ProviderRuntime{
			"ollama": {
				ProviderName: "ollama",
				BaseURL:      "http://127.0.0.1:11434",
				Models:       []config.ModelSpec{{Name: "model-a"}},
			},
		},
	}
	res.SetModelCatalogForTest([]config.ProviderModelGroup{
		{Provider: "ollama", Selectable: true, Active: true, Models: []config.ModelSpec{{Name: "model-a"}}},
	})
	sess := chat.NewSession(res, &nullCompleter{})
	runner := adapter.NewCommandRunner(sess, res, nil)

	out := runner.Run(context.Background(), "model", "nosuchprovider model-x")
	if out.Err == "" {
		t.Fatal("Run(model, nosuchprovider model-x) expected error, got success")
	}
	if !strings.Contains(out.Err, "model-x") {
		t.Fatalf("error must carry the plain-model-name resolution attempt, got %q", out.Err)
	}
}

// TestCommandRunner_SelectModelForProvider_UninitializedRunner covers
// switchModel's uninitialized guard (runner_model.go:196-198) through the
// exported SelectModelForProvider, which - unlike handleModel and SelectModel
// - carries no session/res guard of its own before calling switchModel. A
// runner built with a nil session must fail closed with the standard
// "not initialized" message instead of panicking.
func TestCommandRunner_SelectModelForProvider_UninitializedRunner(t *testing.T) {
	runner := adapter.NewCommandRunner(nil, nil, nil)

	out := runner.SelectModelForProvider(context.Background(), "openrouter", "model-b")
	if out.Err != "session or configuration not initialized" {
		t.Fatalf("SelectModelForProvider on an uninitialized runner = %+v, want Err %q", out, "session or configuration not initialized")
	}
}
