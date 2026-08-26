package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// TestCollectWindowLines_BlockingReadHonorsCancellation is the read_file
// analogue of TestScanLinesWithContext_BlockingReadHonorsCancellation
// (fs_guard_scan_cancel_test.go): before collectWindowLines was wired onto
// scanLinesWithContext, its own `for sc.Scan() { ... }` loop only checked
// ctx.Err() between completed Scan() calls, so a stalled Read (blockingReader
// here never returns on its own) could block collectWindowLines - and its
// real caller, readLineWindow - past caller cancellation. Reuses
// blockingReader/nopCloser from fs_guard_scan_cancel_test.go (same package,
// same blocking-forever pattern chunk 1's tests established) since
// readLineWindow's real file-open path (openRegularFile) rejects FIFOs and
// other special files by design, so a real blocked Read cannot be produced
// through that entrypoint - collectWindowLines is exercised directly instead,
// with the same *bufio.Scanner/closer shape readLineWindow itself uses.
func TestCollectWindowLines_BlockingReadHonorsCancellation(t *testing.T) {
	blockForever := make(chan struct{})
	t.Cleanup(func() { close(blockForever) })

	sc := bufio.NewScanner(&blockingReader{unblock: blockForever})
	closer := &nopCloser{}
	tool := &readFileTool{maxBytes: 0}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	type result struct {
		lines []string
		total int
		err   error
	}
	done := make(chan result, 1)
	go func() {
		lines, total, err := tool.collectWindowLines(ctx, sc, closer, 1, 10)
		done <- result{lines, total, err}
	}()

	select {
	case r := <-done:
		if r.err == nil || !errors.Is(r.err, context.DeadlineExceeded) {
			t.Fatalf("expected a context.DeadlineExceeded error once the deadline passed, got %v", r.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("collectWindowLines did not return within 2s of a 50ms context deadline: the caller is still hung on the blocked read")
	}
	// closer.Close() itself only runs once the abandoned producer goroutine's
	// still-blocked Read() unblocks (via t.Cleanup below, after this test
	// function returns) - scanLinesWithContext's exactly-once close on the
	// abandonment path is already covered directly by
	// TestScanLinesWithContext_BlockingReadHonorsCancellation's sibling
	// tests in fs_guard_scan_cancel_test.go, so it is not re-asserted here.
}

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

// TestReadFileErrTooLongReportsEnforcedBound pins the ErrTooLong message to
// the bound the scanner actually enforces. For an uncapped tool (maxBytes==0)
// the enforced bound is the 1 MiB floor, not "(0 bytes)"; for a small
// configured bound it is max(scannerMax, cap(buf)) = 64 KiB, not the
// configured value.
func TestReadFileErrTooLongReportsEnforcedBound(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	// Uncapped tool: one line longer than the 1 MiB scanner floor.
	big := strings.Repeat("x", 1<<20+1)
	bigPath := filepath.Join(ws.Abs, "bigline.txt")
	if err := os.WriteFile(bigPath, []byte(big+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &readFileTool{ws: ws, maxBytes: 0}
	_, err = tool.readLineWindow(context.Background(), bigPath, 1, 10)
	if err == nil {
		t.Fatal("expected an ErrTooLong error for an oversized line")
	}
	if !strings.Contains(err.Error(), "1048576") {
		t.Fatalf("uncapped read: error must report the 1 MiB enforced bound, got: %v", err)
	}
	if strings.Contains(err.Error(), "(0 bytes)") {
		t.Fatalf("uncapped read: error must not report unenforced maxBytes=0, got: %v", err)
	}

	// Small configured bound: the scanner enforces max(scannerMax, cap(buf))
	// = 64 KiB (bufio.Scanner.Buffer semantics), never the raw 1024.
	big2 := strings.Repeat("y", 64*1024+1)
	big2Path := filepath.Join(ws.Abs, "bigline2.txt")
	if err := os.WriteFile(big2Path, []byte(big2+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool2 := &readFileTool{ws: ws, maxBytes: 1024}
	_, err = tool2.readLineWindow(context.Background(), big2Path, 1, 10)
	if err == nil {
		t.Fatal("expected an ErrTooLong error for an oversized line")
	}
	if !strings.Contains(err.Error(), "65536") {
		t.Fatalf("small bound: error must report the 64 KiB enforced bound, got: %v", err)
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

// TestReadFileWindowCollectionBoundedByBudget pins the fix for
// read-file-window-unbounded-memory: a window call (offset>1, limit=0) on a
// file far larger than maxBytes must bound RETAINED collection to the byte
// budget (plus one line) instead of materializing every line from offset to
// EOF into the windowLines slice that formatWindow renders. Uses the repo's
// runtime.MemStats/runtime.GC() convention (TestRunCommandCaptureMemoryBounded).
// The same call is the negative path: the oversized file is truncated with a
// notice, never fully collected.
//
// The bound is looser than the original 4 MiB: collectWindowLines now scans
// through scanLinesWithContext (fs_guard.go), whose producer goroutine calls
// sc.Text() once per scanned line - for all 200,000 lines here, not just the
// ones inside the window - before batching them onto a channel, since
// bufio.Scanner reuses its internal buffer and the text must be copied out
// before the next Scan() call overwrites it. That is TRANSIENT allocation
// (each string is garbage as soon as its batch's consume calls return); it is
// not retained the way the old bug's unbounded windowLines growth was, but it
// does show up in TotalAlloc, which counts cumulative bytes allocated, GC'd
// or not. 12 MiB gives headroom above that transient per-line cost while
// still catching a real regression back to retaining every line (which would
// run several times higher, from slice growth alone).
func TestReadFileWindowCollectionBoundedByBudget(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// 200,000 lines × 12 bytes ≈ 2.4 MB — far beyond maxBytes. Built before
	// the MemStats baseline so file creation is not counted in the delta.
	var b strings.Builder
	for i := 0; i < 200_000; i++ {
		fmt.Fprintf(&b, "line-%06d\n", i)
	}
	path := filepath.Join(ws.Abs, "many.txt")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	const maxBytes = 4096
	tool := &readFileTool{ws: ws, maxBytes: maxBytes}

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	out, err := tool.readLineWindow(context.Background(), path, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)

	delta := int64(after.TotalAlloc - before.TotalAlloc)
	if delta > 12*1024*1024 {
		t.Fatalf("window collection allocated too much: delta=%d bytes (%d MiB), want < 12 MiB", delta, delta/(1<<20))
	}
	// Output contract unchanged: the truncation notice is present and the
	// result stays within maxBytes plus framing slack.
	if !strings.Contains(out, "truncated at max read size") {
		t.Fatalf("expected truncation notice, got: %q", out)
	}
	if len(out) > maxBytes+256 {
		t.Fatalf("result len=%d exceeds maxBytes+256 framing slack", len(out))
	}
}

func TestReadFileActionableTruncationGuidance(t *testing.T) {
	ws, _ := setupWS(t)
	var b strings.Builder
	for i := 1; i <= 30; i++ {
		fmt.Fprintf(&b, "row-%02d: detailed payload\n", i)
	}
	if err := os.WriteFile(filepath.Join(ws.Abs, "pages.txt"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := NewDefaultRegistry(DefaultOptions{
		Workspace:    ws,
		MaxReadBytes: 150,
	})

	// First page read
	out1, err := reg.Execute(context.Background(), "read_file",
		json.RawMessage(`{"path":"pages.txt","offset":1,"limit":30}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out1, "Call read_file with offset=") {
		t.Fatalf("expected actionable next call offset hint: %q", out1)
	}

	// Parse next offset from notice
	idx := strings.Index(out1, "offset=")
	if idx == -1 {
		t.Fatalf("could not find offset= in %q", out1)
	}
	var nextOffset int
	if _, err := fmt.Sscanf(out1[idx:], "offset=%d", &nextOffset); err != nil {
		t.Fatalf("failed to parse offset from %q: %v", out1[idx:], err)
	}
	if nextOffset <= 1 {
		t.Fatalf("next offset must be > 1, got %d", nextOffset)
	}

	// Follow continuation
	out2, err := reg.Execute(context.Background(), "read_file",
		json.RawMessage(fmt.Sprintf(`{"path":"pages.txt","offset":%d,"limit":30}`, nextOffset)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out2, fmt.Sprintf("… lines %d–", nextOffset)) {
		t.Fatalf("page 2 header must start with nextOffset %d: %q", nextOffset, out2)
	}
}
