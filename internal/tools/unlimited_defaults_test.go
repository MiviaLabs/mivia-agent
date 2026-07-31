package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Tests for the "zero means unlimited" contract: when maxMatches, maxBytes,
// or maxEntries default to 0, the tool returns ALL results without
// truncation. The 256 MiB readClassMaxBytes backstop prevents OOM.

// TestGrepUnlimitedMatchesReturnsAll verifies that grep with maxMatches=0
// returns all matches without a "truncated at N matches" notice.
func TestGrepUnlimitedMatchesReturnsAll(t *testing.T) {
	ws := budgetWorkspace(t)
	for i := 0; i < 60; i++ {
		path := filepath.Join(ws.Abs, fmt.Sprintf("file_%02d.txt", i))
		if err := os.WriteFile(path, []byte("NEEDLE_HERE\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	tool := &grepTool{ws: ws, maxMatches: 0, maxBytes: 256 << 20}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"NEEDLE"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "truncated") {
		t.Fatalf("expected no truncation, got: %s", out)
	}
	count := strings.Count(out, "\n") + 1
	if count != 60 {
		t.Fatalf("expected 60 matches, got %d (output: %s...)", count, truncate(out, 200))
	}
}

// TestGlobUnlimitedMatchesReturnsAll verifies that glob with maxMatches=0
// returns all matches.
func TestGlobUnlimitedMatchesReturnsAll(t *testing.T) {
	ws := budgetWorkspace(t)
	for i := 0; i < 250; i++ {
		path := filepath.Join(ws.Abs, fmt.Sprintf("file_%03d.txt", i))
		if err := os.WriteFile(path, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	tool := &globTool{ws: ws, maxMatches: 0, maxBytes: 256 << 20}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"**/*.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "truncated") {
		t.Fatalf("expected no truncation, got: %s", truncate(out, 200))
	}
	count := strings.Count(out, "\n") + 1
	if count != 250 {
		t.Fatalf("expected 250 matches, got %d", count)
	}
}

// TestReadFileUnlimitedReturnsLargeFile verifies that read_file with
// maxBytes=0 returns the full content of a file larger than the old 256 KiB
// default.
func TestReadFileUnlimitedReturnsLargeFile(t *testing.T) {
	ws := budgetWorkspace(t)
	content := strings.Repeat("A", 512*1024) // 512 KiB
	path := filepath.Join(ws.Abs, "big.txt")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	tool := &readFileTool{ws: ws, maxBytes: 0}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"big.txt"}`))
	if err != nil {
		t.Fatalf("expected no error for large file with maxBytes=0, got: %v", err)
	}
	if len(out) < 512*1024 {
		t.Fatalf("expected at least 512 KiB returned, got %d bytes", len(out))
	}
}

// TestReadFileUnlimitedLongLineViaOffsetLimit verifies that the line-window
// scanner path handles lines >64 KiB when maxBytes=0 (the 1 MiB scanner
// floor prevents the bufio.Scanner.ErrTooLong regression).
func TestReadFileUnlimitedLongLineViaOffsetLimit(t *testing.T) {
	ws := budgetWorkspace(t)
	longLine := strings.Repeat("B", 100*1024) // 100 KiB single line
	path := filepath.Join(ws.Abs, "bigline.txt")
	if err := os.WriteFile(path, []byte(longLine+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	tool := &readFileTool{ws: ws, maxBytes: 0}
	out, err := tool.Execute(context.Background(),
		json.RawMessage(`{"path":"bigline.txt","offset":1,"limit":1}`))
	if err != nil {
		t.Fatalf("expected no error for long line with maxBytes=0, got: %v", err)
	}
	if strings.Contains(out, "exceeds max read size") {
		t.Fatalf("scanner regression: line rejected, got: %s", truncate(out, 200))
	}
}

// TestListDirUnlimitedEntriesReturnsAll verifies that list_dir with
// maxEntries=0 returns all entries.
func TestListDirUnlimitedEntriesReturnsAll(t *testing.T) {
	ws := budgetWorkspace(t)
	for i := 0; i < 600; i++ {
		path := filepath.Join(ws.Abs, fmt.Sprintf("e_%03d", i))
		if err := os.WriteFile(path, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	tool := &listDirTool{ws: ws, maxEntries: 0, maxBytes: 256 << 20}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"."}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "truncated") {
		t.Fatalf("expected no truncation, got: %s", truncate(out, 200))
	}
	count := strings.Count(strings.TrimSpace(out), "\n") + 1
	if count != 600 {
		t.Fatalf("expected 600 entries, got %d", count)
	}
}

// TestGrepBackstopStopsAndNotices verifies that the readClassMaxBytes backstop
// fires when accumulated matches exceed the byte budget. Uses a small budget
// to avoid creating hundreds of MiB of test data.
func TestGrepBackstopStopsAndNotices(t *testing.T) {
	ws := budgetWorkspace(t)
	// Create files with enough matches to exceed a 4096-byte budget.
	for i := 0; i < 10; i++ {
		var sb strings.Builder
		for j := 0; j < 1000; j++ {
			sb.WriteString("NEEDLE\n")
		}
		path := filepath.Join(ws.Abs, fmt.Sprintf("big_%02d.txt", i))
		if err := os.WriteFile(path, []byte(sb.String()), 0644); err != nil {
			t.Fatal(err)
		}
	}
	// Small byte budget to verify the backstop fires without large data.
	tool := &grepTool{ws: ws, maxMatches: 0, maxBytes: 4096}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"NEEDLE"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "truncated at") {
		t.Fatalf("expected byte truncation notice, got result length %d (no notice)", len(out))
	}
	// The result should be bounded near 4096 bytes.
	if len(out) > 4096+256 {
		t.Fatalf("result %d bytes exceeds budget + slack", len(out))
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
