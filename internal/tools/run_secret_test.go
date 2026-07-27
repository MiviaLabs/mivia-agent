package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunCommandBlocksSecretPathArgv(t *testing.T) {
	ws, reg := setupWS(t)
	_ = os.WriteFile(filepath.Join(ws.Abs, ".env"), []byte("SECRET_VALUE=should-not-leak\n"), 0o600)

	out, err := reg.Execute(context.Background(), "run_command", json.RawMessage(`{"argv":["cat",".env"]}`))
	if err == nil {
		t.Fatalf("expected secret path block, got out=%q", out)
	}
	if !strings.Contains(err.Error(), "secret") && !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("err should mention block: %v", err)
	}
	if strings.Contains(out, "SECRET_VALUE") {
		t.Fatalf("must not leak secret content: %q", out)
	}

	// Nested + absolute-ish relative path
	if err := os.MkdirAll(filepath.Join(ws.Abs, "cfg"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(ws.Abs, "cfg", ".env.local"), []byte("X=1\n"), 0o600)
	_, err = reg.Execute(context.Background(), "run_command", json.RawMessage(`{"argv":["cat","cfg/.env.local"]}`))
	if err == nil {
		t.Fatal("expected nested .env block via cat")
	}

	// head / tail same policy
	_, err = reg.Execute(context.Background(), "run_command", json.RawMessage(`{"argv":["head",".env"]}`))
	if err == nil {
		t.Fatal("expected head .env block")
	}

	// Non-secret path still allowed
	_ = os.WriteFile(filepath.Join(ws.Abs, "ok.txt"), []byte("hello\n"), 0o644)
	out, err = reg.Execute(context.Background(), "run_command", json.RawMessage(`{"argv":["cat","ok.txt"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "hello") {
		t.Fatalf("cat ok.txt should work: %q", out)
	}
}

func TestSecretPathInArgv(t *testing.T) {
	if got := secretPathInArgv([]string{".env"}); got != ".env" {
		t.Fatalf("got %q", got)
	}
	if got := secretPathInArgv([]string{"-n", "cfg/.env.local"}); got != "cfg/.env.local" {
		t.Fatalf("got %q", got)
	}
	if got := secretPathInArgv([]string{"-n", "README.md"}); got != "" {
		t.Fatalf("false positive %q", got)
	}
}
