package agent

import (
	"context"
	"testing"

	sdkagentloop "github.com/MiviaLabs/mivia-ai-sdk/agentloop"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
)

// TestToolCallKeyParityAgainstSDKVectors verifies that the host derivation
// (toolCallKeyFromContext and direct ToolCallKey usages) equals
// sdkagentloop.ToolCallKey across representative vectors including blank-ID
// stream calls and full calls.
func TestToolCallKeyParityAgainstSDKVectors(t *testing.T) {
	tests := []struct {
		name         string
		call         sdkshape.ToolCall
		fallbackName string
		wantKey      string
	}{
		{
			name: "full call with id and name",
			call: sdkshape.ToolCall{
				Index:     0,
				ID:        "call_full_123",
				Name:      "read_file",
				Arguments: []byte(`{"path":"main.go"}`),
			},
			fallbackName: "fallback_tool",
			wantKey:      "call_full_123",
		},
		{
			name: "blank-ID stream call (name present, ID empty)",
			call: sdkshape.ToolCall{
				Index:     1,
				ID:        "",
				Name:      "exec_command",
				Arguments: []byte(`{"command":"ls"}`),
			},
			fallbackName: "fallback_tool",
			wantKey:      "exec_command",
		},
		{
			name: "id present without name",
			call: sdkshape.ToolCall{
				ID:   "call_anon_456",
				Name: "",
			},
			fallbackName: "fallback_tool",
			wantKey:      "call_anon_456",
		},
		{
			name: "both ID and Name empty in ctx",
			call: sdkshape.ToolCall{
				ID:   "",
				Name: "",
			},
			fallbackName: "fallback_tool",
			wantKey:      "fallback_tool",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Direct parity with SDK helper
			sdkKey := sdkagentloop.ToolCallKey(tc.call)
			if tc.call.ID != "" || tc.call.Name != "" {
				if sdkKey != tc.wantKey {
					t.Fatalf("sdkagentloop.ToolCallKey(%+v) = %q, want %q", tc.call, sdkKey, tc.wantKey)
				}
			}

			// Host toolCallKeyFromContext derivation parity
			ctx := sdkagentloop.WithToolCall(context.Background(), tc.call)
			got := toolCallKeyFromContext(ctx, tc.fallbackName)
			if got != tc.wantKey {
				t.Fatalf("toolCallKeyFromContext(ctx, %q) = %q, want %q", tc.fallbackName, got, tc.wantKey)
			}
		})
	}

	// Absent call in ctx falls back to fallbackName
	if got := toolCallKeyFromContext(context.Background(), "fallback_tool"); got != "fallback_tool" {
		t.Fatalf("toolCallKeyFromContext(bare ctx, fallback_tool) = %q, want fallback_tool", got)
	}
	if got := toolCallKeyFromContext(nil, "fallback_tool"); got != "fallback_tool" {
		t.Fatalf("toolCallKeyFromContext(nil ctx, fallback_tool) = %q, want fallback_tool", got)
	}
}
