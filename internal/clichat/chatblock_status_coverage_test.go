package clichat

// chatblock_status_coverage_test.go covers the small status helpers in
// chatblock_status.go.

import (
	"errors"

	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

func TestIsWorkStatusBlock(t *testing.T) {
	for _, tc := range []struct {
		b    ChatBlock
		want bool
	}{
		{ChatBlock{Kind: ChatBlockSystem, Text: "→ work"}, true},
		{ChatBlock{Kind: ChatBlockSystem, Text: "no arrow"}, false},
		{ChatBlock{Kind: ChatBlockAssistant}, false},
		{ChatBlock{Kind: ChatBlockUser}, false},
	} {
		if got := IsWorkStatusBlock(tc.b); got != tc.want {
			t.Errorf("IsWorkStatusBlock(%+v) = %v, want %v", tc.b, got, tc.want)
		}
	}
}

func TestHydrateChatBlocksForView(t *testing.T) {
	got := HydrateChatBlocksForView([]provider.Message{{Role: "user", Content: "hi"}})
	if len(got) != 1 {
		t.Fatalf("HydrateChatBlocksForView returned %d blocks, want 1", len(got))
	}
	// Empty input is a no-op.
	if got := HydrateChatBlocksForView(nil); len(got) != 0 {
		t.Fatalf("HydrateChatBlocksForView(nil) returned %d blocks", len(got))
	}
}

func TestReconstructEmptySpeechStatus(t *testing.T) {
	blocks := []ChatBlock{{Kind: ChatBlockAssistant, Text: ""}}
	out := ReconstructEmptySpeechStatus(blocks)
	if len(out) < 1 {
		t.Fatal("ReconstructEmptySpeechStatus must return at least one block")
	}
	// Empty input is a no-op.
	if out := ReconstructEmptySpeechStatus(nil); out != nil {
		t.Fatalf("ReconstructEmptySpeechStatus(nil) = %v, want nil", out)
	}
}

func TestSummaryDisabledReasonEmpty(t *testing.T) {
	// With a fresh resolved config and session, the function returns "".
	if got := SummaryDisabledReason(nil, nil); got != "" {
		t.Errorf("SummaryDisabledReason(nil, nil) = %q, want empty", got)
	}
}

func TestCompactHelpersHandleNilTerminal(t *testing.T) {
	// reportCompactFailure with a nil terminal must write to stderr
	// without panicking.
	reportCompactFailure(nil, errors.New("simulated"))
}
