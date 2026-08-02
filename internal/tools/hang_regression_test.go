package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

func TestReadFileRejectsNamedPipe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("named pipes")
	}
	dir := t.TempDir()
	fifo := filepath.Join(dir, "block.fifo")
	if err := exec.Command("mkfifo", fifo).Run(); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	reg := NewDefaultRegistry(DefaultOptions{Workspace: ws})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	var out string
	var execErr error
	go func() {
		out, execErr = reg.Execute(ctx, "read_file", json.RawMessage(`{"path":"block.fifo"}`))
		close(done)
	}()
	select {
	case <-done:
		if execErr == nil {
			t.Fatalf("expected error for FIFO, out=%q", out)
		}
		if !strings.Contains(execErr.Error(), "regular file") && !strings.Contains(execErr.Error(), "special") {
			t.Fatalf("err=%v", execErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("read_file on FIFO hung past deadline")
	}
}

func TestSearchReplaceRejectsNamedPipe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("named pipes")
	}
	dir := t.TempDir()
	fifo := filepath.Join(dir, "block.fifo")
	if err := exec.Command("mkfifo", fifo).Run(); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	reg := NewDefaultRegistry(DefaultOptions{Workspace: ws})
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	var execErr error
	go func() {
		_, execErr = reg.Execute(ctx, "search_replace", json.RawMessage(
			`{"path":"block.fifo","old_string":"a","new_string":"b"}`))
		close(done)
	}()
	select {
	case <-done:
		if execErr == nil {
			t.Fatal("expected error for FIFO")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("search_replace on FIFO hung")
	}
}

func TestRunCommandCaptureMemoryBounded(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("yes path")
	}
	dir := t.TempDir()
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	const maxOut = 4096
	reg := NewDefaultRegistry(DefaultOptions{
		Workspace: ws, RunAllowlist: []string{"yes"},
		RunTimeoutSec: 1, MaxOutputBytes: maxOut,
	})
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	out, err := reg.Execute(context.Background(), "run_command", json.RawMessage(`{"argv":["yes"]}`))
	if err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)
	delta := int64(after.TotalAlloc - before.TotalAlloc)
	if delta > 32*1024*1024 {
		t.Fatalf("capture allocated too much: delta=%dMB (want << flood)", delta/1e6)
	}
	if !strings.Contains(out, "truncated") {
		head := out
		if len(head) > 200 {
			head = head[:200]
		}
		t.Fatalf("expected truncation notice, out head=%q", head)
	}
	// Result body should stay near product maxOut (header + notice), not multi-MB.
	if len(out) > maxOut+512 {
		t.Fatalf("result len=%d exceeds maxOut+header", len(out))
	}
}

func TestCappedBufferRetainsHeadAndTail(t *testing.T) {
	c := newCappedBuffer(8)
	n, err := c.Write([]byte("abcdefghij"))
	if err != nil || n != 10 {
		t.Fatalf("write n=%d err=%v", n, err)
	}
	// max=8 → headQuota=2, tailQuota=6; body "ab" + elision + "efghij"
	got := string(c.Bytes())
	if !strings.HasPrefix(got, "ab") || !strings.HasSuffix(got, "efghij") {
		t.Fatalf("bytes=%q want head ab + tail efghij", got)
	}
	if !strings.Contains(got, captureElisionMarker) {
		t.Fatalf("missing elision marker: %q", got)
	}
	if !c.Truncated() || c.Written() != 10 {
		t.Fatalf("truncated=%v written=%d", c.Truncated(), c.Written())
	}
}

func TestRequireRegularFileAllowsNormal(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := requireRegularFile(p); err != nil {
		t.Fatal(err)
	}
}
