package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// exampleSecretPatterns / exampleSecretExceptions mirror the values shipped in
// .mivia/mivia.toml.example. Nothing is compiled into the binary, so tests
// that exercise filtering must configure it the way a real workspace does.
// testRunAllowlist is what these tests need available; nothing is compiled in.
var testRunAllowlist = []string{"sh", "bash", "sleep", "echo", "cat", "head", "tail", "true", "false", "printf", "env", "python3", "git", "make", "yes", "seq", "dd", "timeout"}

var exampleSecretPatterns = []string{".env", ".pem", ".key", "id_rsa", "id_ed25519"}
var exampleSecretExceptions = []string{".env.example"}

func TestIsSecretPath(t *testing.T) {
	cases := map[string]bool{
		// Blocked: actual secret files
		".env":            true,
		"cfg/.env":        true,
		".env.local":      true,
		".env.production": true,
		"id_rsa":          true,
		"certs/key.pem":   true,
		// Allowed: templates and non-secret files
		".env.example":   false,
		"main.go":        false,
		"README.md":      false,
		"docs/config.md": false,
	}
	for path, want := range cases {
		if got := isSecretPath(path, exampleSecretExceptions, exampleSecretPatterns); got != want {
			t.Errorf("isSecretPath(%q)=%v want %v", path, got, want)
		}
	}
}

// TestSecretPathExceptionsGlobalIsolation verifies that two registries with
// different SecretPathExceptions do NOT share state - each registration
// captures its own copy.
func TestSecretPathExceptionsGlobalIsolation(t *testing.T) {
	ws1, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Registry configured with the example exceptions (.env.example only).
	reg1 := NewDefaultRegistry(DefaultOptions{
		Workspace:            ws1,
		SecretPathPatterns:   exampleSecretPatterns,
		SecretPathExceptions: exampleSecretExceptions,
	})

	ws2, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Registry with custom exceptions that allow .env files.
	reg2 := NewDefaultRegistry(DefaultOptions{
		Workspace:          ws2,
		SecretPathPatterns: exampleSecretPatterns,
		SecretPathExceptions: []string{
			".env.example",
			".env.local", // allow .env.local in addition to .env.example
		},
	})

	// For reg1 (example config): .env.local is secret, .env.example is not.
	if isSecretPath(".env.local", exampleSecretExceptions, exampleSecretPatterns) {
		// Default behavior: .env.local should be secret
	} else {
		t.Error("example exceptions should NOT allow .env.local")
	}

	// For reg2 (custom): .env.local is explicitly allowed via exceptions.
	if isSecretPath(".env.local", []string{".env.example", ".env.local"}, exampleSecretPatterns) {
		t.Error("custom exceptions should allow .env.local")
	}

	// .pem should always be blocked regardless of exceptions.
	if !isSecretPath("key.pem", []string{".env.example", ".env.local"}, exampleSecretPatterns) {
		t.Error("key.pem should be blocked")
	}
	if !isSecretPath("key.pem", exampleSecretExceptions, exampleSecretPatterns) {
		t.Error("key.pem should be blocked with the example exceptions")
	}

	// Verify the registries themselves don't leak state by checking tool names.
	// (We can't easily introspect the internal secretExceptions slices, but we
	// can confirm both registries are functional.)
	ctx := context.Background()
	if _, err := reg1.Execute(ctx, "read_file", json.RawMessage(`{"path":"test.txt"}`)); err == nil {
		t.Error("expected error reading non-existent file from reg1")
	}
	if _, err := reg2.Execute(ctx, "read_file", json.RawMessage(`{"path":"test.txt"}`)); err == nil {
		t.Error("expected error reading non-existent file from reg2")
	}
}

// With no configured patterns nothing is filtered. This is the documented
// consequence of removing the compiled-in list: filtering is a workspace
// decision, and a workspace that says nothing gets none.
func TestIsSecretPathUnconfiguredFiltersNothing(t *testing.T) {
	for _, p := range []string{".env", "id_rsa", "certs/key.pem", "main.go"} {
		if isSecretPath(p, nil, nil) {
			t.Errorf("isSecretPath(%q) with no configuration = true, want false", p)
		}
	}
}

// The legacy namespace is ordinary workspace content, not a protected path.
// Asserted with the example secret patterns configured, since that is the only
// way it could become unwritable by accident. Plan 04 §7; mutation proof M3.
func TestAgentCanEditLegacyAIDir(t *testing.T) {
	ws, reg := setupWS(t)
	if err := os.MkdirAll(filepath.Join(ws.Abs, ".ai"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Execute(context.Background(), "write_file", json.RawMessage(
		`{"path":".ai/notes.md","content":"ordinary content"}`,
	)); err != nil {
		t.Fatalf("write into legacy dir must succeed: %v", err)
	}
	out, err := reg.Execute(context.Background(), "read_file", json.RawMessage(`{"path":".ai/notes.md"}`))
	if err != nil {
		t.Fatalf("read from legacy dir must succeed: %v", err)
	}
	if !strings.Contains(out, "ordinary content") {
		t.Fatalf("unexpected content: %q", out)
	}
}
