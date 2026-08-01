package provider

import "testing"

func TestDeriveCacheUsageNilUsageIsUnreported(t *testing.T) {
	got := deriveCacheUsage(nil, CacheStyleImplicit)
	if got.Reported {
		t.Fatalf("nil usage must be unreported, got %+v", got)
	}
	if got != (CacheUsage{}) {
		t.Fatalf("nil usage must be the zero value, got %+v", got)
	}
}

func TestDeriveCacheUsageNoRecognizedFieldsIsUnreported(t *testing.T) {
	got := deriveCacheUsage(&usageWire{PromptTokens: 100}, CacheStyleImplicit)
	if got.Reported {
		t.Fatalf("usage with only prompt_tokens must be unreported, got %+v", got)
	}
}

func TestDeriveCacheUsageDeepSeekFlatShape(t *testing.T) {
	hit, miss := 80, 20
	usage := &usageWire{PromptTokens: 100, PromptCacheHitTokens: &hit, PromptCacheMissTokens: &miss}
	got := deriveCacheUsage(usage, CacheStyleImplicit)
	want := CacheUsage{Reported: true, Style: CacheStyleImplicit, InputTokens: 100, CachedInputTokens: 80, CacheWriteTokens: 0}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// PromptTokensDetails absent entirely (DeepSeek's real shape carries no such
// object at all) must not panic when only the flat fields are present.
func TestDeriveCacheUsageDeepSeekShapeWithNilDetailsDoesNotPanic(t *testing.T) {
	hit := 5
	usage := &usageWire{PromptTokens: 10, PromptCacheHitTokens: &hit, PromptTokensDetails: nil}
	got := deriveCacheUsage(usage, CacheStyleImplicit)
	if !got.Reported || got.CachedInputTokens != 5 {
		t.Fatalf("got %+v", got)
	}
}

func TestDeriveCacheUsageOpenAINestedShape(t *testing.T) {
	cached, write := 30, 10
	usage := &usageWire{PromptTokens: 200, PromptTokensDetails: &promptTokensDetailsWire{CachedTokens: &cached, CacheWriteTokens: &write}}
	got := deriveCacheUsage(usage, CacheStyleImplicit)
	want := CacheUsage{Reported: true, Style: CacheStyleImplicit, InputTokens: 200, CachedInputTokens: 30, CacheWriteTokens: 10}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// When both conventions are present and disagree, the flat DeepSeek-style
// field wins for CachedInputTokens - it is the more specific convention.
// CacheWriteTokens has no flat equivalent, so it always comes from the
// nested shape when present.
func TestDeriveCacheUsageBothConventionsFlatWinsForCached(t *testing.T) {
	flatHit, nestedCached, nestedWrite := 80, 999, 15
	usage := &usageWire{
		PromptTokens: 100, PromptCacheHitTokens: &flatHit,
		PromptTokensDetails: &promptTokensDetailsWire{CachedTokens: &nestedCached, CacheWriteTokens: &nestedWrite},
	}
	got := deriveCacheUsage(usage, CacheStyleImplicit)
	if got.CachedInputTokens != 80 {
		t.Fatalf("flat field must win, got CachedInputTokens=%d", got.CachedInputTokens)
	}
	if got.CacheWriteTokens != 15 {
		t.Fatalf("write tokens must come from the nested shape, got %d", got.CacheWriteTokens)
	}
}

// A misbehaving upstream sending a negative token count must not produce
// negative accounting - it is clamped to zero rather than propagated.
func TestDeriveCacheUsageClampsNegativeTokenCounts(t *testing.T) {
	negative := -5
	usage := &usageWire{PromptTokens: -1, PromptCacheHitTokens: &negative}
	got := deriveCacheUsage(usage, CacheStyleImplicit)
	if got.InputTokens < 0 || got.CachedInputTokens < 0 {
		t.Fatalf("negative token counts must be clamped to zero, got %+v", got)
	}
}

func TestDeriveCacheUsagePromptCacheMissOnlyIsReported(t *testing.T) {
	miss := 100
	got := deriveCacheUsage(&usageWire{PromptTokens: 100, PromptCacheMissTokens: &miss}, CacheStyleImplicit)
	if !got.Reported {
		t.Fatalf("a miss-only response still confirms the provider reports cache accounting, got %+v", got)
	}
	if got.CachedInputTokens != 0 {
		t.Fatalf("miss-only must not imply any cached tokens, got %+v", got)
	}
}
