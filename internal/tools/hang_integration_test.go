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

// Cross-tool integration regressions (registry + real FS/process) for hang classes.
// Complements hang_regression_test.go unit coverage and agent loop integrations.

func TestIntegration_Tools_FIFORejectedAcrossSurface(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("named pipes")
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

	cases := []struct {
		name string
		tool string
		args string
	}{
		{"read", "read_file", `{"path":"block.fifo"}`},
		{"read_window", "read_file", `{"path":"block.fifo","offset":1,"limit":10}`},
		{"replace", "search_replace", `{"path":"block.fifo","old_string":"a","new_string":"b"}`},
		{"write_existing", "write_file", `{"path":"block.fifo","content":"x"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			done := make(chan struct{})
			var out string
			var execErr error
			go func() {
				out, execErr = reg.Execute(ctx, tc.tool, json.RawMessage(tc.args))
				close(done)
			}()
			select {
			case <-done:
				if execErr == nil {
					t.Fatalf("expected error, out=%q", out)
				}
				msg := strings.ToLower(execErr.Error())
				if !strings.Contains(msg, "regular") && !strings.Contains(msg, "special") && !strings.Contains(msg, "directory") {
					// write_file may error via open/stat wording
					if !strings.Contains(msg, "not a regular") {
						t.Fatalf("%s err=%v", tc.tool, execErr)
					}
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("%s hung on FIFO", tc.tool)
			}
		})
	}
}

func TestIntegration_Tools_CatNLargeFileBounded(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("cat")
	}
	dir := t.TempDir()
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	const maxOut = 4096
	reg := NewDefaultRegistry(DefaultOptions{
		Workspace:      ws,
		RunAllowlist:   DefaultAllowlist,
		RunTimeoutSec:  15,
		MaxOutputBytes: maxOut,
	})
	// ~2 MiB plain text
	payload := strings.Repeat("line content for cat -n\n", 80_000)
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start := time.Now()
	out, err := reg.Execute(context.Background(), "run_command",
		json.RawMessage(`{"argv":["cat","-n","big.txt"]}`))
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)
	delta := int64(after.TotalAlloc - before.TotalAlloc)

	if elapsed > 20*time.Second {
		t.Fatalf("cat -n took %s", elapsed)
	}
	if delta > 48*1024*1024 {
		t.Fatalf("alloc delta too large: %dMB", delta/1e6)
	}
	if !strings.Contains(out, "exit=0") && !strings.Contains(out, "exit=timeout") {
		// exit=0 expected if cat finishes; timeout acceptable on slow CI
		t.Fatalf("unexpected status body: %q", out[:min(len(out), 200)])
	}
	if !strings.Contains(out, "truncated") && len(out) > maxOut+1024 {
		t.Fatalf("unbounded result len=%d", len(out))
	}
	if len(out) > maxOut+2048 {
		t.Fatalf("result too large len=%d", len(out))
	}
}

func TestIntegration_Tools_GrepSkipsFIFO(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("named pipes")
	}
	dir := t.TempDir()
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hit.txt"), []byte("findme\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("mkfifo", filepath.Join(dir, "block.fifo")).Run(); err != nil {
		t.Fatal(err)
	}
	reg := NewDefaultRegistry(DefaultOptions{Workspace: ws})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan struct{})
	var out string
	var execErr error
	go func() {
		out, execErr = reg.Execute(ctx, "grep", json.RawMessage(`{"pattern":"findme","path":"."}`))
		close(done)
	}()
	select {
	case <-done:
		if execErr != nil {
			t.Fatal(execErr)
		}
		if !strings.Contains(out, "hit.txt") {
			t.Fatalf("expected match in hit.txt, got %q", out)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("grep hung scanning workspace with FIFO present")
	}
}
