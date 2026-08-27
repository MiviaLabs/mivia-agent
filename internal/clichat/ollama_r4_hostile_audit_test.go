package clichat

// Hostile functional audit of the Round-4 ollama changes
// (1e4c756b..4b745468): runConfiguredChatOnce against a genuinely closed
// loopback port must surface a provider/dial error (never a missing-key
// error, never a panic); doctor human screens; keyed setup through
// runSetupWithIO. TEST-ONLY.

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cliorchestrate "github.com/MiviaLabs/mivia-agent/internal/cliorchestrate"
	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// closedLoopbackPort reserves a 127.0.0.1 port and releases it, so every

// writeOllamaChatConfig writes a HOME-isolated single-provider ollama config

// runChatOnceGuarded runs runConfiguredChatOnceImpl and turns a panic into a
// test failure: the Round-4 contract is no panic on any of these paths.
// runConfiguredChatOnce chdirs into the workspace, so the process cwd is
// restored before the tempdir cleanup can delete it.
func runChatOnceGuarded(t *testing.T, res *config.Resolved, ws string) (err error) {
	t.Helper()
	orig, werr := os.Getwd()
	if werr != nil {
		t.Fatalf("getwd before chat: %v", werr)
	}
	defer func() { _ = os.Chdir(orig) }()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("runConfiguredChatOnce PANICKED: %v", r)
		}
	}()
	return runConfiguredChatOnceImpl(chatInvocation{prompt: "hi", workspacePath: ws, noTools: true}, res)
}

// Focus 1a: keyless ollama at http://127.0.0.1:<closed>/v1 must fail with a
// provider DIAL error - not a missing-key error and not a panic.
func TestR4ChatOnceClosedPort127SurfacesDialError(t *testing.T) {
	addr := closedLoopbackPort(t)
	res, ws := writeOllamaChatConfig(t, "http://"+addr+"/v1")

	start := time.Now()
	err := runChatOnceGuarded(t, res, ws)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("closed loopback port: expected a dial error, got nil")
	}
	if strings.Contains(err.Error(), "missing API key") {
		t.Fatalf("key gate fired for keyless loopback: %v", err)
	}
	if !wantDialRefused(err) {
		t.Fatalf("error = %q, want a connection-refused provider/dial error", err)
	}
	if elapsed > 60*time.Second {
		t.Fatalf("closed-port chat failure took %v - unbounded retry?", elapsed)
	}
	t.Logf("127.0.0.1 closed-port chat error surfaced in %v: %v", elapsed, err)
}

// Focus 1b: http://localhost:<closed>/v1 resolves to 127.0.0.1 on this host,
// so construction succeeds and the dial fails fast with a provider error.
func TestR4ChatOnceClosedPortLocalhostSurfacesDialError(t *testing.T) {
	addr := closedLoopbackPort(t)
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	res, ws := writeOllamaChatConfig(t, "http://localhost:"+port+"/v1")

	start := time.Now()
	err = runChatOnceGuarded(t, res, ws)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("closed localhost port: expected a dial error, got nil")
	}
	if strings.Contains(err.Error(), "missing API key") {
		t.Fatalf("key gate fired for keyless localhost: %v", err)
	}
	if !wantDialRefused(err) {
		t.Fatalf("error = %q, want a connection-refused provider/dial error", err)
	}
	if elapsed > 60*time.Second {
		t.Fatalf("closed localhost chat failure took %v - unbounded retry?", elapsed)
	}
	t.Logf("localhost closed-port chat error surfaced in %v: %v", elapsed, err)
}

// Focus 1c: cloud ollama with no key must fail at the key gate BEFORE any
// dial; the gate error text is produced before provider construction.
func TestR4ChatOnceCloudNoKeyFailsAtGate(t *testing.T) {
	res, ws := writeOllamaChatConfig(t, "https://ollama.com/v1")
	err := runChatOnceGuarded(t, res, ws)
	if err == nil {
		t.Fatal("cloud ollama with no key: expected missing-key error, got nil")
	}
	if !strings.Contains(err.Error(), "missing API key: set OLLAMA_API_KEY") {
		t.Fatalf("err = %v, want the key-gate message naming OLLAMA_API_KEY", err)
	}
	if strings.Contains(err.Error(), "connection refused") || strings.Contains(err.Error(), "request failed") {
		t.Fatalf("err = %v: a dial happened before the key gate", err)
	}
}

// Focus 2: doctor human screen for loopback ollama (localhost) with no key
// reports the keyless local-daemon state - not a missing-key error, not a
// crash, and status ok.
func TestR4DoctorLoopbackLocalhostKeyless(t *testing.T) {
	cfgPath := writeOllamaDoctorConfig(t, "http://localhost:11434/v1")
	t.Setenv("OLLAMA_API_KEY", "")
	var stdout, stderr bytes.Buffer
	if err := cliorchestrate.RunDoctorWithIO([]string{"--config", cfgPath, "--workspace", t.TempDir()}, &stdout, &stderr); err != nil {
		t.Fatalf("doctor error for loopback ollama = %v (want ok)", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "api_key:    not required (local daemon)") {
		t.Fatalf("stdout missing keyless line:\n%s", out)
	}
	if !strings.Contains(out, "status:     ok") {
		t.Fatalf("stdout missing 'status:     ok':\n%s", out)
	}
	if strings.Contains(out, "MISSING") {
		t.Fatalf("stdout reports MISSING for keyless loopback:\n%s", out)
	}
	if strings.Contains(stderr.String(), "not ready for chat") {
		t.Fatalf("stderr reports 'not ready for chat' for keyless loopback:\n%s", stderr.String())
	}
}

// Focus 2: doctor human screen for cloud ollama with no key reports the
// credential as unavailable and the doctor status error names the env var.
func TestR4DoctorCloudNoKeyReportsMissing(t *testing.T) {
	cfgPath := writeOllamaDoctorConfig(t, "https://ollama.com/v1")
	t.Setenv("OLLAMA_API_KEY", "")
	var stdout, stderr bytes.Buffer
	err := cliorchestrate.RunDoctorWithIO([]string{"--config", cfgPath, "--workspace", t.TempDir()}, &stdout, &stderr)
	if err == nil {
		t.Fatal("doctor must report a non-ok status for cloud ollama with no key")
	}
	if !strings.Contains(err.Error(), "OLLAMA_API_KEY") {
		t.Fatalf("doctor status error = %v, want it to name OLLAMA_API_KEY", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "api_key:    MISSING - set OLLAMA_API_KEY") {
		t.Fatalf("stdout missing MISSING line:\n%s", out)
	}
	if !strings.Contains(stderr.String(), "not ready for chat") {
		t.Fatalf("stderr missing 'not ready for chat':\n%s", stderr.String())
	}
}

// writeOllamaDoctorConfig writes an ollama doctor fixture and returns its path.
func writeOllamaDoctorConfig(t *testing.T, baseURL string) string {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, ".mivia", "mivia.toml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`[provider]
name = "ollama"

[providers.ollama]
base_url = %q
models = [{ name = "qwen3:8b", context_window_tokens = 32768 }]
default_model = "qwen3:8b"
`, baseURL)
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfgPath
}
