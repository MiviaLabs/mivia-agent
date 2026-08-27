package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// Audit regression for commit 0f6e524: with the default tools config
// (max_read_bytes = 262144) the runtime dispatcher's hidden output ceiling
// defaulted to the same 262144 bytes, so read_file's line-window path -
// content up to max_read_bytes PLUS the "… lines X–Y" header and truncation
// notice - exceeded the ceiling by ~63–111 bytes and the ENTIRE result was
// destroyed with {"error":"output budget exceeded","status":"failed"}.
// That broke the documented recovery path: "file too large … re-call with
// offset and limit" led straight into a destroyed result.
//
// This test reproduces the audit probe through the production composition:
// default registry + the agent loop's fallback dispatcher (Options.Dispatcher
// nil → runtime.NewToolDispatcher(reg, runtime.Policy{}), exactly
// loop_tools.go's path). The window read of a 300KB file must succeed with an
// honest header, not be replaced by "output budget exceeded".
func TestIntegration_WideWindowReadNotDestroyedByDispatcherCeiling(t *testing.T) {
	h := newIntegrationHelperWithOpts(t, []scriptedStep{
		{
			content: "reading a window of the large file",
			toolCalls: []provider.ToolCall{toolCall("call_window", "read_file",
				`{"path":"big.txt","offset":1,"limit":100000}`)},
		},
		{content: "window read complete"},
	}, tools.DefaultOptions{MaxReadBytes: 256 * 1024})
	// 300KB of 99-byte lines + newline, mirroring the audit probe file. At
	// 100 bytes/line the window content stops 45 bytes under the 262144
	// budget, so header + content + truncation notice lands ~262164 bytes -
	// past the old dispatcher default ceiling of exactly 262144.
	line := strings.Repeat("a", 99) + "\n"
	var b strings.Builder
	for b.Len() < 300*1024 {
		b.WriteString(line)
	}
	if err := os.WriteFile(filepath.Join(h.ws.Abs, "big.txt"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	loop := h.newLoop()
	// No Options.Dispatcher: the loop builds the production fallback
	// dispatcher itself. No MaxToolResultChars: default uncapped config.
	_, err := loop.Run(context.Background(), "read a window of big.txt", Options{
		Model:              "integration-model",
		MaxSteps:           5,
		MaxConcurrentTools: 2,
		ToolTimeout:        10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	body, ok := toolResultsByName(loop.Messages)["call_window"]
	if !ok {
		t.Fatalf("missing tool result for call_window; msgs=%+v", loop.Messages)
	}
	if strings.Contains(body, "output budget exceeded") {
		t.Fatalf("dispatcher destroyed a config-compliant window read: %q", body)
	}
	if !strings.HasPrefix(body, "… lines 1–") {
		head := body
		if len(head) > 120 {
			head = head[:120]
		}
		t.Fatalf("window result missing honest \"… lines X–Y\" header, head=%q", head)
	}
	// The window of a 300KB file at the 262144-byte default budget must carry
	// real content near the budget, proving the result was not shrunk to an
	// error envelope.
	if len(body) < 200*1024 {
		t.Fatalf("window result suspiciously small (%d bytes); expected ~max_read_bytes of content", len(body))
	}
	// The tool's own honest framing must arrive intact: its truncation notice
	// closes the window, and the loop must not have tail-cut the result (its
	// marker is "... (truncated N bytes)").
	if !strings.Contains(body, "... truncated at max read size (262144 bytes") || !strings.Contains(body, "Call read_file with offset=") {
		tail := body
		if len(tail) > 120 {
			tail = tail[len(tail)-120:]
		}
		t.Fatalf("window result missing the tool's truncation notice, tail=%q", tail)
	}
	if strings.Contains(body, "... (truncated") {
		t.Fatalf("loop tail-cut the tool result; capability truncation bound misdeclared")
	}
}
