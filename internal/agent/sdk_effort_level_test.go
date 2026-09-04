package agent

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
)

// TestSDKEffortToLevelCoversTheWholeVocabulary: the SDK's four efforts
// each map to a CLI level, and anything outside that vocabulary maps to
// the unset Level - which sends no reasoning field at all. Defaulting to
// a real level instead would silently spend reasoning budget the caller
// never asked for, on a value the CLI does not understand.
func TestSDKEffortToLevelCoversTheWholeVocabulary(t *testing.T) {
	for _, tc := range []struct {
		in   sdkshape.ReasoningEffort
		want reasoning.Level
	}{
		{sdkshape.ReasoningEffortNone, reasoning.Off},
		{sdkshape.ReasoningEffortLow, reasoning.Low},
		{sdkshape.ReasoningEffortMedium, reasoning.Medium},
		{sdkshape.ReasoningEffortHigh, reasoning.High},
		{sdkshape.ReasoningEffort("ludicrous"), ""},
		{sdkshape.ReasoningEffort(""), ""},
	} {
		if got := sdkEffortToLevel(tc.in); got != tc.want {
			t.Errorf("sdkEffortToLevel(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
