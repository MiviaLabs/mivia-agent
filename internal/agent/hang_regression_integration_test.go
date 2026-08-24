package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	appruntime "github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// Integration regressions for tool hang classes:
//  1. soft FS timeouts (named pipes / special files)
//  2. run_command unbounded capture (flood / cat -n large files)
//
// These wire the real agent Loop + httptest provider + DefaultRegistry tools
// (same stack as loop_integration_test.go).

func toolCall(id, name, args string) provider.ToolCall {
	return provider.ToolCall{
		ID:   id,
		Type: "function",
		Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: name, Arguments: args},
	}
}

func toolResultsByName(msgs []provider.Message) map[string]string {
	out := make(map[string]string)
	for _, m := range msgs {
		if m.Role == provider.RoleTool {
			out[m.Name] = m.Content
			// Prefer ID-keyed when multiple same-name tools run.
			if m.ToolCallID != "" {
				out[m.ToolCallID] = m.Content
			}
		}
	}
	return out
}

// TestIntegration_ReadFileNamedPipeDoesNotHangTurn: model calls read_file on a
// FIFO; agent turn must finish quickly with a failed tool result (not pin workers).
func TestIntegration_ReadFileNamedPipeDoesNotHangTurn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("named pipes")
	}
	h := newIntegrationHelper(t, []scriptedStep{
		{
			content:   "reading pipe",
			toolCalls: []provider.ToolCall{toolCall("call_fifo", "read_file", `{"path":"block.fifo"}`)},
		},
		{content: "handled non-regular path"},
	})
	fifo := filepath.Join(h.ws.Abs, "block.fifo")
	if err := exec.Command("mkfifo", fifo).Run(); err != nil {
		t.Fatal(err)
	}

	loop := h.newLoop()
	start := time.Now()
	text, err := loop.Run(context.Background(), "read the fifo", Options{
		Model:              "integration-model",
		MaxSteps:           5,
		MaxConcurrentTools: 2,
		ToolTimeout:        2 * time.Second,
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("agent turn hung on FIFO read_file: %s", elapsed)
	}
	if text == "" {
		t.Fatal("expected final assistant text")
	}

	results := toolResultsByName(loop.Messages)
	body, ok := results["call_fifo"]
	if !ok {
		t.Fatalf("missing tool result for call_fifo; msgs=%+v", loop.Messages)
	}
	low := strings.ToLower(body)
	// A failure may reach the model either as a raw tool error body or as the
	// dispatcher's bounded {"status":"failed"} envelope; both tell the model the
	// call did not succeed, which is the property under test.
	if !strings.Contains(low, "error") && !strings.Contains(low, "regular file") && !strings.Contains(low, "special") && !strings.Contains(low, `"status":"failed"`) {
		t.Fatalf("expected non-regular/error tool body, got %q", body)
	}
}

// TestIntegration_ParallelFIFODoesNotPinSiblingTools: FIFO read_file in the same
// batch as a normal read_file must not block the sibling or the batch.
func TestIntegration_ParallelFIFODoesNotPinSiblingTools(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("named pipes")
	}
	h := newIntegrationHelper(t, []scriptedStep{
		{
			content: "parallel reads",
			toolCalls: []provider.ToolCall{
				toolCall("call_fifo", "read_file", `{"path":"block.fifo"}`),
				toolCall("call_ok", "read_file", `{"path":"ok.txt"}`),
			},
		},
		{content: "both tools finished"},
	})
	if err := os.WriteFile(filepath.Join(h.ws.Abs, "ok.txt"), []byte("sibling-ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("mkfifo", filepath.Join(h.ws.Abs, "block.fifo")).Run(); err != nil {
		t.Fatal(err)
	}

	loop := h.newLoop()
	start := time.Now()
	_, err := loop.Run(context.Background(), "read both", Options{
		Model:              "integration-model",
		MaxSteps:           5,
		MaxConcurrentTools: 2,
		ToolTimeout:        2 * time.Second,
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("parallel batch hung: %s", elapsed)
	}

	results := toolResultsByName(loop.Messages)
	if !strings.Contains(results["call_ok"], "sibling-ok") {
		t.Fatalf("sibling read_file failed or missing: %q", results["call_ok"])
	}
	fifoBody := strings.ToLower(results["call_fifo"])
	if !strings.Contains(fifoBody, "error") && !strings.Contains(fifoBody, "regular") && !strings.Contains(fifoBody, "special") && !strings.Contains(fifoBody, `"status":"failed"`) {
		t.Fatalf("fifo tool expected error body, got %q", results["call_fifo"])
	}
}

// TestIntegration_SearchReplaceNamedPipeDoesNotHangTurn covers the write-path
// soft-timeout hang class through the full agent loop.
func TestIntegration_SearchReplaceNamedPipeDoesNotHangTurn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("named pipes")
	}
	h := newIntegrationHelper(t, []scriptedStep{
		{
			content: "editing pipe",
			toolCalls: []provider.ToolCall{toolCall("call_sr", "search_replace",
				`{"path":"block.fifo","old_string":"a","new_string":"b"}`)},
		},
		{content: "edit rejected"},
	})
	if err := exec.Command("mkfifo", filepath.Join(h.ws.Abs, "block.fifo")).Run(); err != nil {
		t.Fatal(err)
	}

	loop := h.newLoop()
	start := time.Now()
	_, err := loop.Run(context.Background(), "edit fifo", Options{
		Model:       "integration-model",
		MaxSteps:    5,
		ToolTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("search_replace on FIFO hung: %s", elapsed)
	}
	body := strings.ToLower(toolResultsByName(loop.Messages)["call_sr"])
	if !strings.Contains(body, "error") && !strings.Contains(body, "regular") && !strings.Contains(body, "special") && !strings.Contains(body, `"status":"failed"`) {
		t.Fatalf("expected error tool body, got %q", body)
	}
}

func writeDenseTextFile(t *testing.T, abs string, size int) {
	t.Helper()
	line := strings.Repeat("x", 80) + "\n"
	var b strings.Builder
	for b.Len() < size {
		b.WriteString(line)
	}
	if err := os.WriteFile(abs, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertCatNBoundedResult(t *testing.T, body string, maxOut int, elapsed time.Duration, allocDelta int64) {
	t.Helper()
	if elapsed > 20*time.Second {
		t.Fatalf("cat -n large file took too long: %s", elapsed)
	}
	if allocDelta > 64*1024*1024 {
		t.Fatalf("capture allocated too much during cat -n: delta=%dMB", allocDelta/1e6)
	}
	if body == "" {
		t.Fatal("missing run_command tool result")
	}
	if !strings.Contains(body, "command:") || !strings.Contains(body, "cat") {
		head := body
		if len(head) > 120 {
			head = head[:120]
		}
		t.Fatalf("unexpected run_command body head: %q", head)
	}
	if !strings.Contains(body, "truncated") && len(body) > maxOut+1024 {
		t.Fatalf("expected truncation or bounded body, len=%d", len(body))
	}
	if len(body) > maxOut+4096 {
		t.Fatalf("tool result still huge: len=%d", len(body))
	}
}

// TestIntegration_RunCommandCatNLargeFileMemoryBounded: agent executes
// run_command cat -n on a multi-MB file; turn completes with truncated body and
// without multi-GB capture allocations.
func TestIntegration_RunCommandCatNLargeFileMemoryBounded(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("cat path")
	}
	const maxOut = 8192
	h := newIntegrationHelper(t, []scriptedStep{
		{
			content: "cat -n large",
			toolCalls: []provider.ToolCall{toolCall("call_cat", "run_command",
				`{"argv":["cat","-n","large.txt"]}`)},
		},
		{content: "done with cat"},
	})
	h.reg = tools.NewDefaultRegistry(tools.DefaultOptions{
		Workspace: h.ws, RunAllowlist: []string{"sh", "bash", "sleep", "echo", "cat", "yes", "true", "timeout", "printf"},
		RunTimeoutSec: 10, MaxOutputBytes: maxOut,
	})
	writeDenseTextFile(t, filepath.Join(h.ws.Abs, "large.txt"), 4<<20)

	disp, err := appruntime.NewToolDispatcher(h.reg, appruntime.Policy{MaxOutputBytes: 512 << 10})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(disp.Close)

	loop := h.newLoop()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start := time.Now()
	_, err = loop.Run(context.Background(), "number lines of large.txt", Options{
		Model: "integration-model", MaxSteps: 5, MaxConcurrentTools: 2,
		ToolTimeout: 15 * time.Second, Dispatcher: disp, MaxToolResultChars: maxOut + 2048,
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)
	assertCatNBoundedResult(t, toolResultsByName(loop.Messages)["call_cat"], maxOut,
		time.Since(start), int64(after.TotalAlloc-before.TotalAlloc))
}

// TestIntegration_RunCommandYesFloodDoesNotHangLoop: flooding stdout via yes
// ends at process timeout with bounded result (agent loop + dispatcher).
func TestIntegration_RunCommandYesFloodDoesNotHangLoop(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("yes path")
	}
	const maxOut = 4096
	h := newIntegrationHelper(t, []scriptedStep{
		{
			content:   "flood",
			toolCalls: []provider.ToolCall{toolCall("call_yes", "run_command", `{"argv":["yes"]}`)},
		},
		{content: "flood handled"},
	})
	h.reg = tools.NewDefaultRegistry(tools.DefaultOptions{
		Workspace:      h.ws,
		RunAllowlist:   []string{"yes"},
		RunTimeoutSec:  1,
		MaxOutputBytes: maxOut,
	})
	disp, err := appruntime.NewToolDispatcher(h.reg, appruntime.Policy{MaxOutputBytes: 256 << 10})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(disp.Close)

	loop := h.newLoop()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start := time.Now()
	_, err = loop.Run(context.Background(), "run yes", Options{
		Model:       "integration-model",
		MaxSteps:    5,
		Dispatcher:  disp,
		ToolTimeout: 5 * time.Second,
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)
	delta := int64(after.TotalAlloc - before.TotalAlloc)

	if elapsed > 5*time.Second {
		t.Fatalf("yes flood hung loop: %s", elapsed)
	}
	if delta > 32*1024*1024 {
		t.Fatalf("yes flood allocated too much: delta=%dMB", delta/1e6)
	}
	body := toolResultsByName(loop.Messages)["call_yes"]
	if !strings.Contains(body, "exit=timeout") && !strings.Contains(body, "exit=canceled") {
		t.Fatalf("expected timeout/canceled status, got %q", body[:min(len(body), 200)])
	}
	if !strings.Contains(body, "truncated") {
		t.Fatalf("expected truncation notice, got head %q", body[:min(len(body), 200)])
	}
}
