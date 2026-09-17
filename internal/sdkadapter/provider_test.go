package sdkadapter

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider/reasoning"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
)

// TestLevelToReasoningEffort pins the CLI Level -> SDK ReasoningEffort bridge:
// the four levels the SDK exposes a constant for and the empty Level map to a
// known SDK effort with ok=true; the three CLI-only levels and any unknown
// Level are refused (empty effort, ok=false) so the caller can tell "no SDK
// surface" apart from "no level picked".
func TestLevelToReasoningEffort(t *testing.T) {
	tests := []struct {
		name   string
		in     reasoning.Level
		want   sdkshape.ReasoningEffort
		wantOK bool
	}{
		{"empty -> empty, true", "", "", true},
		{"off -> none, true", reasoning.Off, sdkshape.ReasoningEffortNone, true},
		{"low -> low, true", reasoning.Low, sdkshape.ReasoningEffortLow, true},
		{"medium -> medium, true", reasoning.Medium, sdkshape.ReasoningEffortMedium, true},
		{"high -> high, true", reasoning.High, sdkshape.ReasoningEffortHigh, true},
		{"minimal -> empty, false", reasoning.Minimal, "", false},
		{"xhigh -> empty, false", reasoning.XHigh, "", false},
		{"max -> empty, false", reasoning.Max, "", false},
		{"auto -> empty, false", reasoning.Auto, "", false},
		{"unknown -> empty, false", reasoning.Level("nuclear"), "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := LevelToReasoningEffort(tt.in)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if got != tt.want {
				t.Fatalf("effort = %q, want %q", got, tt.want)
			}
		})
	}
}
