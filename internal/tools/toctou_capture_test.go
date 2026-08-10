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

// Regression: dualCapture shares one maxOut budget across stdout+stderr.
func TestDualCaptureSharedBudgetNotTwiceMax(t *testing.T) {
	const max = 100
	d := newDualCapture(max)
	// Flood both streams beyond max each.
	n1, _ := d.Stdout().Write([]byte(strings.Repeat("A", 80)))
	n2, _ := d.Stderr().Write([]byte(strings.Repeat("B", 80)))
	if n1 != 80 || n2 != 80 {
		t.Fatalf("Write must accept full payloads: n1=%d n2=%d", n1, n2)
	}
	if d.Retained() > max {
		t.Fatalf("retained=%d > max=%d", d.Retained(), max)
	}
	if !d.Truncated() {
		t.Fatal("expected truncated")
	}
	// Combined may include elision markers; body retain still ≤ max.
	// Prefix of stdout head retained first (stdout writes first into head).
	if !strings.HasPrefix(d.StdoutString(), "A") {
		t.Fatalf("stdout=%q", d.StdoutString())
	}
	// Late stderr should keep a tail of B under the shared budget.
	if !strings.Contains(d.StderrString(), "B") {
		t.Fatalf("stderr lost entirely: %q", d.StderrString())
	}
}

// Regression: run_command dual-stream flood retains ≤ maxOut (not 2×).
func TestRunCommandDualStreamSharedCaptureBudget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh path")
	}
	dir := t.TempDir()
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	const maxOut = 4096
	reg := NewDefaultRegistry(DefaultOptions{
		Workspace: ws, RunAllowlist: []string{"sh"},
		RunTimeoutSec: 2, MaxOutputBytes: maxOut,
	})
	// Write large streams to both stdout and stderr.
	args := map[string]any{
		"argv": []string{"sh", "-c", `dd if=/dev/zero bs=1024 count=200 2>/dev/null | tr '\0' A; dd if=/dev/zero bs=1024 count=200 2>/dev/null | tr '\0' B >&2`},
	}
	raw, _ := json.Marshal(args)
	out, err := reg.Execute(context.Background(), "run_command", raw)
	if err != nil {
		t.Fatal(err)
	}
	// Strip header lines to measure body-ish size.
	if !strings.Contains(out, "truncated") {
		t.Fatalf("expected truncation notice: %q", out[:min(len(out), 200)])
	}
	// Body after status should not be ~2*maxOut of retained capture.
	if len(out) > maxOut+1024 {
		t.Fatalf("result len=%d exceeds shared budget + header slack", len(out))
	}
}

// Regression: the memory backstop limits capture when max_output_bytes is zero.
func TestRunCommandMemoryBackstopLimitsUncappedCapture(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh path")
	}
	dir := t.TempDir()
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	const backstop = 4096
	reg := NewDefaultRegistry(DefaultOptions{
		Workspace: ws, RunAllowlist: []string{"sh"},
		RunTimeoutSec: 2, MemoryBackstopBytes: backstop,
	})
	raw, _ := json.Marshal(map[string]any{
		"argv": []string{"sh", "-c", `dd if=/dev/zero bs=1024 count=200 2>/dev/null | tr '\0' A; dd if=/dev/zero bs=1024 count=200 2>/dev/null | tr '\0' B >&2`},
	})
	out, err := reg.Execute(context.Background(), "run_command", raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "truncated at 2457 bytes") {
		t.Fatalf("result has no true backstop notice: %q", out[:min(len(out), 300)])
	}
	if len(out) > backstop+1024 {
		t.Fatalf("result len=%d exceeds backstop + header slack", len(out))
	}
}

// Regression: openRegularFile does not block on FIFO (Unix O_NONBLOCK).
func TestOpenRegularFileFIFONoHang(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix nonblock")
	}
	dir := t.TempDir()
	fifo := filepath.Join(dir, "block.fifo")
	if err := exec.Command("mkfifo", fifo).Run(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var openErr error
	go func() {
		_, _, openErr = openRegularFile(fifo)
		close(done)
	}()
	select {
	case <-done:
		if openErr == nil {
			t.Fatal("expected error for FIFO")
		}
		if !strings.Contains(openErr.Error(), "regular") && !strings.Contains(openErr.Error(), "special") {
			// O_NONBLOCK open of FIFO may succeed then fstat fails as non-regular.
			if !strings.Contains(strings.ToLower(openErr.Error()), "fifo") &&
				!strings.Contains(openErr.Error(), "named pipe") &&
				!strings.Contains(openErr.Error(), "mode") {
				t.Fatalf("err=%v", openErr)
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("openRegularFile hung on FIFO")
	}
}

// Regression: write_file to existing FIFO fails quickly (no block).
func TestWriteFileExistingFIFONoHang(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix fifo")
	}
	dir := t.TempDir()
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, "block.fifo")
	if err := exec.Command("mkfifo", fifo).Run(); err != nil {
		t.Fatal(err)
	}
	reg := NewDefaultRegistry(DefaultOptions{Workspace: ws})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan struct{})
	var execErr error
	go func() {
		_, execErr = reg.Execute(ctx, "write_file", json.RawMessage(`{"path":"block.fifo","content":"x"}`))
		close(done)
	}()
	select {
	case <-done:
		if execErr == nil {
			t.Fatal("expected write_file error on FIFO")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("write_file hung on FIFO")
	}
}

// Regression: TOCTOU open path - readLineWindow uses openRegularFile, not bare Open.
func TestReadFileWindowFIFONoHang(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix fifo")
	}
	dir := t.TempDir()
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("mkfifo", filepath.Join(dir, "block.fifo")).Run(); err != nil {
		t.Fatal(err)
	}
	reg := NewDefaultRegistry(DefaultOptions{Workspace: ws})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan struct{})
	var execErr error
	go func() {
		_, execErr = reg.Execute(ctx, "read_file",
			json.RawMessage(`{"path":"block.fifo","offset":1,"limit":5}`))
		close(done)
	}()
	select {
	case <-done:
		if execErr == nil {
			t.Fatal("expected error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("read_file window hung on FIFO")
	}
}

// Race-ish: create FIFO at path after a regular file was planned - openRegularFileWrite must not hang.
func TestOpenRegularFileWriteFIFONoHang(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix fifo")
	}
	dir := t.TempDir()
	fifo := filepath.Join(dir, "race.fifo")
	if err := exec.Command("mkfifo", fifo).Run(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var openErr error
	go func() {
		_, _, openErr = openRegularFileWrite(fifo, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
		close(done)
	}()
	select {
	case <-done:
		if openErr == nil {
			t.Fatal("expected error writing FIFO")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("openRegularFileWrite hung on FIFO")
	}
}
