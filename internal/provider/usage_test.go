package provider

import "testing"

func TestDeriveTokenUsageNilUsageIsUnreported(t *testing.T) {
	got := deriveTokenUsage(nil)
	if got.Reported {
		t.Fatalf("nil usage must be unreported, got %+v", got)
	}
	if got != (TokenUsage{}) {
		t.Fatalf("nil usage must be the zero value, got %+v", got)
	}
}

func TestDeriveTokenUsageWithPromptAndCompletion(t *testing.T) {
	completion := 50
	got := deriveTokenUsage(&usageWire{PromptTokens: 100, CompletionTokens: &completion})
	want := TokenUsage{Reported: true, InputTokens: 100, OutputTokens: 50}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestDeriveTokenUsageOnlyPromptTokens(t *testing.T) {
	got := deriveTokenUsage(&usageWire{PromptTokens: 100})
	want := TokenUsage{Reported: true, InputTokens: 100, OutputTokens: 0}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// A misbehaving upstream sending a negative completion token count must not
// produce negative accounting - it is clamped to zero rather than propagated,
// matching deriveCacheUsage's handling of negative cache counts.
func TestDeriveTokenUsageClampsNegativeCompletionTokens(t *testing.T) {
	completion := -5
	got := deriveTokenUsage(&usageWire{PromptTokens: 100, CompletionTokens: &completion})
	want := TokenUsage{Reported: true, InputTokens: 100, OutputTokens: 0}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestDeriveTokenUsageOnlyCompletionTokens(t *testing.T) {
	completion := 50
	got := deriveTokenUsage(&usageWire{PromptTokens: 0, CompletionTokens: &completion})
	want := TokenUsage{Reported: true, InputTokens: 0, OutputTokens: 50}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestDeriveTokenUsageNonNilButAllZeroIsUnreported(t *testing.T) {
	got := deriveTokenUsage(&usageWire{})
	if got.Reported {
		t.Fatalf("zero-value usageWire must be unreported, got %+v", got)
	}
	if got != (TokenUsage{}) {
		t.Fatalf("zero-value usageWire must return zero TokenUsage, got %+v", got)
	}
}

// Cache-accounting fields without any real prompt/completion token count must
// NOT mark token usage as reported. A cache-only observation would otherwise
// surface as a genuine zero-input reading and poison the calibration EWMA.
func TestDeriveTokenUsageCacheOnlyIsUnreported(t *testing.T) {
	hit := 500
	cached := 400
	cacheWrite := 100
	for _, usage := range []*usageWire{
		{PromptCacheHitTokens: &hit},
		{PromptTokensDetails: &promptTokensDetailsWire{CachedTokens: &cached}},
		{PromptTokensDetails: &promptTokensDetailsWire{CacheWriteTokens: &cacheWrite}},
	} {
		got := deriveTokenUsage(usage)
		if got.Reported {
			t.Fatalf("cache-only usage %+v must be unreported token usage, got %+v", usage, got)
		}
		if got != (TokenUsage{}) {
			t.Fatalf("cache-only usage %+v must yield zero TokenUsage, got %+v", usage, got)
		}
	}
}

// A cache-miss field is still a real prompt-token observation, so it keeps
// reporting token usage even when the flat prompt_tokens field is absent.
func TestDeriveTokenUsageCacheMissStillReports(t *testing.T) {
	miss := 300
	got := deriveTokenUsage(&usageWire{PromptCacheMissTokens: &miss})
	if !got.Reported {
		t.Fatalf("cache-miss usage must be reported token usage, got %+v", got)
	}
	if got.InputTokens != 0 || got.OutputTokens != 0 {
		t.Fatalf("cache-miss-only usage token counts = %+v, want zeros", got)
	}
}
