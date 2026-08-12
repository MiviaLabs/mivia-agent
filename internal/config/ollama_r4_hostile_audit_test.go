package config

// Hostile functional audit of the Round-4 ollama changes: the config layer
// must keep the literal loopback predicate (no DNS resolution at load), so
// the keyless loopback entry stays SELECTABLE at load even though provider
// construction later fails closed under a hostile resolver. Also pins the
// 17-combo selectability matrix size. TEST-ONLY.

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
)

// TestR4ConfigLoadDoesNotResolveDNS pins that Load never resolves DNS for the
// ollama loopback selectability decision: a hostile resolver that fails any
// lookup would break Load if the layer resolved hostnames. The provider layer
// resolves once at construction; the config layer stays literal-only.
func TestR4ConfigLoadDoesNotResolveDNS(t *testing.T) {
	orig := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			return nil, fmt.Errorf("config layer dialed DNS (%s %s); ollama selectability must stay literal-only", network, address)
		},
	}
	t.Cleanup(func() { net.DefaultResolver = orig })

	dir := t.TempDir()
	path := filepath.Join(dir, "mivia.toml")
	body := `[provider]
name = "ollama"

[providers.ollama]
base_url = "http://localhost:11434/v1"
models = [{ name = "qwen3:8b", context_window_tokens = 32768 }]
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	os.Unsetenv("OLLAMA_API_KEY")
	t.Setenv("HOME", t.TempDir())

	res, err := Load(LoadOptions{ConfigPath: path})
	if err != nil {
		t.Fatalf("Load must not resolve DNS for the loopback predicate: %v", err)
	}
	group := findProviderGroup(res.ModelCatalog(), "ollama")
	if group == nil || !group.Selectable {
		t.Fatalf("loopback ollama must stay selectable at load without DNS (group=%+v)", group)
	}
	if group.DisabledReason != "" {
		t.Fatalf("DisabledReason = %q, want empty", group.DisabledReason)
	}
}

// TestR4OllamaSelectableMatrixHas17Combos pins the audit-matrix size so the
// matrix cannot silently shrink below the 17 documented combinations.
func TestR4OllamaSelectableMatrixHas17Combos(t *testing.T) {
	if got := len(ollamaSelectableMatrix); got != 17 {
		t.Fatalf("ollamaSelectableMatrix has %d combos, want 17", got)
	}
}
