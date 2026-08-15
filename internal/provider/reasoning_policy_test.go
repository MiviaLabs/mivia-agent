package provider

import "testing"

// TestReasoningPolicyForUnawareCompleterIsZeroValue proves the fallback for a
// Completer that does not implement ReasoningPolicyAware (every hand-written
// test fake, and any provider this package has never heard of) is "no
// replay, no reject" - never a false positive that would repair history a
// provider never asked to have repaired.
func TestReasoningPolicyForUnawareCompleterIsZeroValue(t *testing.T) {
	var unaware Completer = unawareCompleter{}
	if got := ReasoningPolicyFor(unaware); got != (ReasoningPolicy{}) {
		t.Fatalf("ReasoningPolicyFor(unaware completer) = %+v, want zero value", got)
	}
	if got := ReasoningPolicyFor(nil); got != (ReasoningPolicy{}) {
		t.Fatalf("ReasoningPolicyFor(nil) = %+v, want zero value", got)
	}
}

// TestReasoningPolicyForDeepSeekMirrorsConstructionOptions pins that the
// capability surface mirrors the exact CompatOptions bits NewDeepSeek sets:
// both replay and reject are on, independent of configured reasoning effort.
func TestReasoningPolicyForDeepSeekMirrorsConstructionOptions(t *testing.T) {
	c, err := NewDeepSeek(Options{APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	got := ReasoningPolicyFor(c)
	want := ReasoningPolicy{RequiresReplay: true, RejectReasoningLess: true}
	if got != want {
		t.Fatalf("ReasoningPolicyFor(deepseek) = %+v, want %+v", got, want)
	}
}

// TestReasoningPolicyForZAILikeClientRejectsWithoutDropping pins the z.ai
// shape referenced throughout api_message.go: replay on, reject off, so a
// reasoning=off multi-step tool run still ships those turns instead of
// having them dropped.
func TestReasoningPolicyForZAILikeClientRejectsWithoutDropping(t *testing.T) {
	c := NewOpenAICompatWithOptions(CompatOptions{
		Name: "zai", BaseURL: "https://example.invalid/v1", APIKey: "k",
		RequiresReasoningReplay: true,
	})
	got := ReasoningPolicyFor(c)
	want := ReasoningPolicy{RequiresReplay: true, RejectReasoningLess: false}
	if got != want {
		t.Fatalf("ReasoningPolicyFor(zai-like) = %+v, want %+v", got, want)
	}
}
