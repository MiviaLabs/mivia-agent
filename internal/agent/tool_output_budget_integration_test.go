package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// Audit regression round 2, end to end. Commit 2dca36b derived the
// dispatcher's output backstop from the result budgets tools DECLARE.
// list_dir, glob and write_file declared none — they capped results by entry
// count, match count, and request size, none of which bounds bytes — so a
// deep tree, a directory of long names, or an overwrite of a large file
// produced results the dispatcher destroyed whole, and the model was handed
// {"error":"output budget exceeded","status":"failed"} instead.
//
// These tests run the real agent loop with no Options.Dispatcher, so the loop
// builds the production fallback dispatcher itself (loop_tools.go →
// runtime.NewToolDispatcher(reg, runtime.Policy{})), exactly as commit
// 2dca36b's read_file test does.

// TestIntegration_LargeGlobReachesModelTruncatedNotDestroyed uses the DEFAULT
// tools config: no operator change was ever needed for this one.
func TestIntegration_LargeGlobReachesModelTruncatedNotDestroyed(t *testing.T) {
	h := newIntegrationHelper(t, []scriptedStep{
		{
			content:   "finding the markdown files",
			toolCalls: []provider.ToolCall{toolCall("call_glob", "glob", `{"pattern":"**/*.md"}`)},
		},
		{content: "glob complete"},
	})
	// A chain of 200-byte directory components makes every matched path
	// ~1.7KB, so glob's hardcoded 200-match cap bounds no number of bytes.
	deep := h.ws.Abs
	for i := 0; i < 8; i++ {
		deep = filepath.Join(deep, strings.Repeat("d", 200))
	}
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	stem := strings.Repeat("f", 95)
	for i := 0; i < 250; i++ {
		if err := os.WriteFile(filepath.Join(deep, fmt.Sprintf("%s%03d.md", stem, i)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	body := runToolStep(t, h, "list the markdown files", "call_glob")
	if strings.Contains(body, "output budget exceeded") {
		t.Fatalf("dispatcher destroyed a default-config glob result: %q", body)
	}
	if !strings.Contains(body, stem) {
		t.Fatalf("glob result reached the model without any real paths; head=%q", head(body))
	}
	if !strings.Contains(body, "... truncated at ") {
		t.Fatalf("glob result claims completeness it does not have; tail=%q", tail(body))
	}
	if strings.Contains(body, "... (truncated") {
		t.Fatalf("loop tail-cut the tool result; capability truncation bound misdeclared: tail=%q", tail(body))
	}
}

// TestIntegration_LargeListDirReachesModelTruncatedNotDestroyed raises
// max_list_dir_entries, a first-class [tools] knob, exactly as the confirmed
// reproduction did. The listing must arrive truncated with an accurate
// omitted-entry count, not destroyed.
func TestIntegration_LargeListDirReachesModelTruncatedNotDestroyed(t *testing.T) {
	h := newIntegrationHelper(t, []scriptedStep{
		{
			content:   "listing the directory",
			toolCalls: []provider.ToolCall{toolCall("call_list", "list_dir", `{"path":"."}`)},
		},
		{content: "listing complete"},
	})
	// The helper's registry uses default options; this defect needs the
	// operator knob raised, so rebuild the registry over the same workspace.
	h.reg = tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: h.ws, MaxListDirEntries: 5000})
	stem := strings.Repeat("n", 100)
	const total = 4000
	for i := 0; i < total; i++ {
		if err := os.WriteFile(filepath.Join(h.ws.Abs, fmt.Sprintf("%s%04d", stem, i)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	body := runToolStep(t, h, "list the workspace", "call_list")
	if strings.Contains(body, "output budget exceeded") {
		t.Fatalf("dispatcher destroyed a config-compliant listing: %q", body)
	}
	if !strings.Contains(body, stem+"0000") {
		t.Fatalf("listing reached the model without real entries; head=%q", head(body))
	}
	lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	var budget, omitted int
	if n, err := fmt.Sscanf(lines[len(lines)-1], "... truncated at %d bytes (%d more)", &budget, &omitted); n != 2 || err != nil {
		t.Fatalf("listing lacks an honest byte-truncation notice; tail=%q", tail(body))
	}
	if delivered := len(lines) - 1; delivered+omitted != total {
		t.Fatalf("listing claims %d delivered + %d omitted = %d, but the directory holds %d",
			delivered, omitted, delivered+omitted, total)
	}
	if strings.Contains(body, "... (truncated") {
		t.Fatalf("loop tail-cut the listing: tail=%q", tail(body))
	}
}

// TestIntegration_LargeOverwriteDiffReachesModelTruncatedNotDestroyed: the
// write happens on disk before the result is built, so destroying the result
// told the model a completed write had failed. Default config.
func TestIntegration_LargeOverwriteDiffReachesModelTruncatedNotDestroyed(t *testing.T) {
	h := newIntegrationHelper(t, []scriptedStep{
		{
			content:   "shrinking the file",
			toolCalls: []provider.ToolCall{toolCall("call_write", "write_file", `{"path":"bulk.txt","content":"tiny\n"}`)},
		},
		{content: "write complete"},
	})
	var bulk strings.Builder
	for i := 0; i < 4000; i++ {
		fmt.Fprintf(&bulk, "old line %d %s\n", i, strings.Repeat("o", 80))
	}
	target := filepath.Join(h.ws.Abs, "bulk.txt")
	if err := os.WriteFile(target, []byte(bulk.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	body := runToolStep(t, h, "replace bulk.txt", "call_write")
	if strings.Contains(body, "output budget exceeded") {
		t.Fatalf("dispatcher destroyed the result of a write that actually happened: %q", body)
	}
	if !strings.HasPrefix(body, "wrote bulk.txt (") {
		t.Fatalf("write confirmation lost; head=%q", head(body))
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "tiny\n" {
		t.Fatalf("file content = %q; the model's result must describe the real write", string(data))
	}
}

// runToolStep drives one scripted tool call through the loop and returns the
// model-visible tool result body.
func runToolStep(t *testing.T, h *integrationHelper, prompt, callID string) string {
	t.Helper()
	loop := h.newLoop()
	if _, err := loop.Run(context.Background(), prompt, Options{
		Model:              "integration-model",
		MaxSteps:           5,
		MaxConcurrentTools: 2,
		ToolTimeout:        20 * time.Second,
	}); err != nil {
		t.Fatal(err)
	}
	body, ok := toolResultsByName(loop.Messages)[callID]
	if !ok {
		t.Fatalf("missing tool result for %s; msgs=%+v", callID, loop.Messages)
	}
	return body
}

func head(s string) string {
	if len(s) > 160 {
		return s[:160]
	}
	return s
}

func tail(s string) string {
	if len(s) > 160 {
		return s[len(s)-160:]
	}
	return s
}
