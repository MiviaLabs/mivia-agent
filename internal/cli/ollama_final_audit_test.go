package cli

// Final (Round-6) regression pins for the ollama loopback keyless gate. The
// Round-6 auditors verified both behaviors via overlay but could not commit
// the pins from that environment; this file makes them durable. TEST-ONLY:
// touches no production code.

import (
	"bytes"
	"context"
	"fmt"
	cliorchestrate "github.com/MiviaLabs/mivia-agent/internal/cliorchestrate"
	"net"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// TestFinalAuditChatGateNonOllamaLoopbackStillFires pins that the keyless
// loopback relaxation applies ONLY to the ollama provider. A hand-built
// Resolved with a loopback base_url but a non-ollama provider (deepseek) and
// no key must still fail at the missing-key gate, naming the provider's API
// key env var. If the gate ever keyed the relaxation off the base URL alone,
// this error would not fire and a loopback URL would silently unlock a
// non-ollama provider without a credential.
func TestFinalAuditChatGateNonOllamaLoopbackStillFires(t *testing.T) {
	res := &config.Resolved{
		ProviderName: "deepseek",
		BaseURL:      "http://127.0.0.1:11434/v1", // loopback literal, but provider is NOT ollama
		APIKeyEnv:    "DEEPSEEK_API_KEY",
		APIKeySet:    false,
	}
	err := runConfiguredChatOnceImpl(chatInvocation{prompt: "hi", workspacePath: t.TempDir(), noTools: true}, res)
	if err == nil {
		t.Fatal("chat with a non-ollama provider at a loopback base_url and no key must fail at the missing-key gate, got nil")
	}
	if !strings.Contains(err.Error(), "missing API key") {
		t.Fatalf("error = %q, want it to contain \"missing API key\"", err)
	}
	if !strings.Contains(err.Error(), "DEEPSEEK_API_KEY") {
		t.Fatalf("error = %q, want it to name DEEPSEEK_API_KEY", err)
	}
}

// TestFinalAuditDoctorHostileResolverKeylessAndCloud pins that doctor is
// display-only: with every DNS lookup failing (hostile resolver, mirroring
// TestR4ConfigLoadDoesNotResolveDNS), the keyless loopback screen must still
// exit 0 and print the local-daemon explanation with status ok, and the cloud
// screen must still report a MISSING state naming OLLAMA_API_KEY. No
// resolution error may leak into stdout/stderr, and doctor must not crash or
// surface the resolver.
func TestFinalAuditDoctorHostileResolverKeylessAndCloud(t *testing.T) {
	orig := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			return nil, fmt.Errorf("hostile resolver (%s %s); doctor must not resolve hostnames", network, address)
		},
	}
	t.Cleanup(func() { net.DefaultResolver = orig })

	t.Setenv("HOME", t.TempDir())
	t.Setenv("OLLAMA_API_KEY", "")

	// Keyless loopback ollama: exit 0, local-daemon explanation, status ok.
	loopbackCfg := writeOllamaDoctorConfig(t, "http://127.0.0.1:11434/v1")
	var loopbackOut, loopbackErr bytes.Buffer
	if err := cliorchestrate.RunDoctorWithIO([]string{"--config", loopbackCfg, "--workspace", t.TempDir()}, &loopbackOut, &loopbackErr); err != nil {
		t.Fatalf("doctor error for keyless loopback ollama under a hostile resolver = %v (want ok)", err)
	}
	if !strings.Contains(loopbackOut.String(), "not required (local daemon)") {
		t.Fatalf("loopback stdout missing 'not required (local daemon)':\n%s", loopbackOut.String())
	}
	if !strings.Contains(loopbackOut.String(), "status:     ok") {
		t.Fatalf("loopback stdout missing 'status:     ok':\n%s", loopbackOut.String())
	}

	// Cloud ollama with no key: MISSING state naming OLLAMA_API_KEY.
	cloudCfg := writeOllamaDoctorConfig(t, "https://ollama.com/v1")
	var cloudOut, cloudErr bytes.Buffer
	err := cliorchestrate.RunDoctorWithIO([]string{"--config", cloudCfg, "--workspace", t.TempDir()}, &cloudOut, &cloudErr)
	if err == nil {
		t.Fatal("doctor must report a non-ok status for cloud ollama with no key")
	}
	if !strings.Contains(err.Error(), "OLLAMA_API_KEY") {
		t.Fatalf("doctor status error = %v, want it to name OLLAMA_API_KEY", err)
	}
	if !strings.Contains(cloudOut.String(), "api_key:    MISSING - set OLLAMA_API_KEY") {
		t.Fatalf("cloud stdout missing MISSING line:\n%s", cloudOut.String())
	}

	// Doctor must never surface the resolver or its failures (display-only).
	for name, out := range map[string]string{
		"loopback stdout": loopbackOut.String(),
		"loopback stderr": loopbackErr.String(),
		"cloud stdout":    cloudOut.String(),
		"cloud stderr":    cloudErr.String(),
	} {
		for _, leaked := range []string{"hostile resolver", "no such host", "lookup ", "server misbehaving", "i/o timeout"} {
			if strings.Contains(out, leaked) {
				t.Fatalf("%s leaks resolution error text %q:\n%s", name, leaked, out)
			}
		}
	}
}
