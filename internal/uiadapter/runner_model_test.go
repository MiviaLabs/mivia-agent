package uiadapter_test

// runner_model_test.go closes the diff-coverage gap in runner_model.go:
// handleModel's empty-catalog guard, availableModelsByProvider's per-group
// skip/fallback branches, resolveProviderAndModel's provider-runtime-prefix
// and slash-splitting resolution paths, the exact-name search's Selectable
// skip, the single-other-provider switch-failure hint, and SelectModel's
// discarded-reasoning-override notice. See the package doc comment at the
// top of runner_model.go for the branches these lines belong to.

import (
	"context"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
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
	runner := uiadapter.NewCommandRunner(sess, res, nil)

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
	runner := uiadapter.NewCommandRunner(sess, res, nil)

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
	runner := uiadapter.NewCommandRunner(sess, res, nil)

	out := runner.SelectModel(context.Background(), "openrouter/model-b")
	if out.Err == "" {
		t.Fatalf("expected a downstream switch error (openrouter has no catalog entry), got success: %+v", out)
	}
	if !strings.Contains(out.Err, `"model-b" (openrouter)`) {
		t.Fatalf("error must name the resolved provider/model pair, got %q", out.Err)
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
	runner := uiadapter.NewCommandRunner(sess, res, nil)

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
	runner := uiadapter.NewCommandRunner(sess, res, nil)

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
	runner := uiadapter.NewCommandRunner(sess, res, nil)

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
	runner := uiadapter.NewCommandRunner(sess, res, nil)

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
