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

func TestReadFileWindowLineNumbers(t *testing.T) {
	ws, reg := setupWS(t)
	body := "aaa\nbbb\nccc\n"
	if err := os.WriteFile(filepath.Join(ws.Abs, "nums.txt"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := reg.Execute(context.Background(), "read_file",
		json.RawMessage(`{"path":"nums.txt","offset":1,"limit":3}`))
	if err != nil {
		t.Fatal(err)
	}
	// Header must have total count.
	if !strings.Contains(out, "of 3") {
		t.Fatalf("header missing total count: %q", out)
	}
	lines := strings.Split(out, "\n")
	// header + 3 content lines.
	if len(lines) != 4 {
		t.Fatalf("expected 4 lines (header + 3 content), got %d: %q", len(lines), out)
	}
	// Right-aligned line numbers: width=1, so "N | content".
	if lines[1] != "1 | aaa" {
		t.Fatalf("line 1: %q", lines[1])
	}
	if lines[2] != "2 | bbb" {
		t.Fatalf("line 2: %q", lines[2])
	}
	if lines[3] != "3 | ccc" {
		t.Fatalf("line 3: %q", lines[3])
	}
}

func TestReadFileWindowLineNumbersWidth(t *testing.T) {
	ws, reg := setupWS(t)
	// 100 lines → width=3, so "  1 | ..." " 99 | ..." "100 | ...".
	var b strings.Builder
	for i := 1; i <= 100; i++ {
		fmt.Fprintf(&b, "line-%03d\n", i)
	}
	if err := os.WriteFile(filepath.Join(ws.Abs, "wide.txt"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := reg.Execute(context.Background(), "read_file",
		json.RawMessage(`{"path":"wide.txt","offset":98,"limit":3}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "of 100") {
		t.Fatalf("header missing total: %q", out)
	}
	lines := strings.Split(out, "\n")
	// header + 3 content lines.
	if len(lines) != 4 {
		t.Fatalf("expected 4 lines, got %d", len(lines))
	}
	if lines[1] != " 98 | line-098" {
		t.Fatalf("line 98: %q", lines[1])
	}
	if lines[2] != " 99 | line-099" {
		t.Fatalf("line 99: %q", lines[2])
	}
	if lines[3] != "100 | line-100" {
		t.Fatalf("line 100: %q", lines[3])
	}
}

func TestReadFileWindowOffsetFromMiddle(t *testing.T) {
	ws, reg := setupWS(t)
	body := "a\nb\nc\nd\ne\n"
	if err := os.WriteFile(filepath.Join(ws.Abs, "mid.txt"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := reg.Execute(context.Background(), "read_file",
		json.RawMessage(`{"path":"mid.txt","offset":3,"limit":2}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "of 5") {
		t.Fatalf("header missing total: %q", out)
	}
	lines := strings.Split(out, "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines (header + 2 content), got %d: %q", len(lines), out)
	}
	if lines[1] != "3 | c" {
		t.Fatalf("line 3: %q", lines[1])
	}
	if lines[2] != "4 | d" {
		t.Fatalf("line 4: %q", lines[2])
	}
	// Verify the header range.
	if !strings.HasPrefix(out, "… lines 3–4 of 5") {
		t.Fatalf("wrong header: %q", out)
	}
}

func TestReadFileWindowSingleLineFile(t *testing.T) {
	ws, reg := setupWS(t)
	if err := os.WriteFile(filepath.Join(ws.Abs, "one.txt"), []byte("only line"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := reg.Execute(context.Background(), "read_file",
		json.RawMessage(`{"path":"one.txt","offset":1,"limit":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "of 1") {
		t.Fatalf("header missing total: %q", out)
	}
	lines := strings.Split(out, "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(lines), out)
	}
	if lines[1] != "1 | only line" {
		t.Fatalf("line 1: %q", lines[1])
	}
}

func TestReadFileWindowTrailingNewline(t *testing.T) {
	ws, reg := setupWS(t)
	// "a\n" → Scanner yields 1 token ("a"). Trailing newline is NOT a
	// separate line for Scanner semantics. Total should be 1.
	if err := os.WriteFile(filepath.Join(ws.Abs, "trail.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := reg.Execute(context.Background(), "read_file",
		json.RawMessage(`{"path":"trail.txt","offset":1,"limit":10}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "of 1") {
		t.Fatalf("header should report 1 scanner line, got: %q", out)
	}
	lines := strings.Split(out, "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines (header + 1 content), got %d: %q", len(lines), out)
	}
	if lines[1] != "1 | a" {
		t.Fatalf("line 1: %q", lines[1])
	}
}

func TestReadFileWindowEmptyFile(t *testing.T) {
	ws, reg := setupWS(t)
	if err := os.WriteFile(filepath.Join(ws.Abs, "empty.txt"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := reg.Execute(context.Background(), "read_file",
		json.RawMessage(`{"path":"empty.txt","offset":1,"limit":10}`))
	if err != nil {
		t.Fatalf("expected no error for empty file window, got: %v", err)
	}
	if out != "" {
		t.Fatalf("expected empty output, got: %q", out)
	}
}

func TestReadFileWindowFullFileStillRaw(t *testing.T) {
	ws, reg := setupWS(t)
	content := "line1\nline2\nline3\n"
	if err := os.WriteFile(filepath.Join(ws.Abs, "raw.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	// Full-file path (no offset/limit) must return verbatim content,
	// no line numbers.
	out, err := reg.Execute(context.Background(), "read_file",
		json.RawMessage(`{"path":"raw.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	if out != content {
		t.Fatalf("full-file read must be verbatim, got: %q", out)
	}
}

func TestReadFileWindowTruncationLineNumbers(t *testing.T) {
	ws, _ := setupWS(t)
	var b strings.Builder
	for i := 1; i <= 20; i++ {
		fmt.Fprintf(&b, "line-%02d\n", i)
	}
	if err := os.WriteFile(filepath.Join(ws.Abs, "trunc.txt"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := NewDefaultRegistry(DefaultOptions{
		Workspace:    ws,
		MaxReadBytes: 50,
	})
	out, err := reg.Execute(context.Background(), "read_file",
		json.RawMessage(`{"path":"trunc.txt","offset":1,"limit":20}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "truncated at max read size") {
		t.Fatalf("expected truncation notice: %q", out)
	}
	// Header range must reflect only the formatted lines, not all 20.
	if !strings.HasPrefix(out, "… lines 1–") {
		t.Fatalf("header should start with range: %q", out)
	}
	if !strings.Contains(out, "of 20") {
		t.Fatalf("header should have total 20: %q", out)
	}
	// Every content line must have a line number prefix.
	contentLines := strings.Split(out, "\n")
	for i, cl := range contentLines[1:] {
		if i == len(contentLines)-2 && strings.Contains(cl, "truncated") {
			continue // skip truncation notice
		}
		if cl == "" {
			continue
		}
		if !strings.Contains(cl, " | ") {
			t.Fatalf("content line %d missing line number: %q", i+1, cl)
		}
	}
}

// TestReadFileWindowWidthMinimum pins the width floor in formatWindow: a
// single-line file (totalLines=1) renders with width 1, so the content line
// carries a "1 | " prefix even when maxBytes is very large and nothing
// truncates.
func TestReadFileWindowWidthMinimum(t *testing.T) {
	ws, reg := setupWSWithOpts(t, DefaultOptions{MaxReadBytes: 1 << 20})
	if err := os.WriteFile(filepath.Join(ws.Abs, "single.txt"), []byte("only line"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := reg.Execute(context.Background(), "read_file",
		json.RawMessage(`{"path":"single.txt","offset":1,"limit":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "of 1") {
		t.Fatalf("header missing total count: %q", out)
	}
	if !strings.Contains(out, "1 | only line") {
		t.Fatalf("expected width-1 line prefix, got: %q", out)
	}
}

func TestReadFileWindowOffsetPastEndReportsTotal(t *testing.T) {
	ws, reg := setupWS(t)
	body := "a\nb\nc\n"
	if err := os.WriteFile(filepath.Join(ws.Abs, "short.txt"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := reg.Execute(context.Background(), "read_file",
		json.RawMessage(`{"path":"short.txt","offset":10,"limit":1}`))
	if err == nil {
		t.Fatal("expected offset-past-end error")
	}
	if !strings.Contains(err.Error(), "3 lines") {
		t.Fatalf("error should report total line count, got: %v", err)
	}
}
