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
