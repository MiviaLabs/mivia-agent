package chat

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// TestPrefixIdentityToolSchemaDigestStableForEqualRegistry pins INV-68-1: the
// digest is stable for an equal registry and for equal tool sets built
// independently in the same registration order (json.Marshal sorts object
// keys). Registration order is wire-affecting - the tools array is emitted in
// order - so a different order MUST change the digest: an order-insensitive
// digest would miss a wire change and break identity equality's sufficiency.
func TestPrefixIdentityToolSchemaDigestStableForEqualRegistry(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(fixedBodyTool{name: "read_file"})
	reg.Register(fixedBodyTool{name: "grep"})
	first := toolSchemaDigest(reg)
	again := toolSchemaDigest(reg)
	if first != again {
		t.Fatalf("repeated digest of the same registry changed: %s vs %s", first, again)
	}

	twin := tools.NewRegistry()
	twin.Register(fixedBodyTool{name: "read_file"})
	twin.Register(fixedBodyTool{name: "grep"})
	if got := toolSchemaDigest(twin); got != first {
		t.Fatalf("equal tool sets in the same order produced different digests: %s vs %s", got, first)
	}

	reordered := tools.NewRegistry()
	reordered.Register(fixedBodyTool{name: "grep"})
	reordered.Register(fixedBodyTool{name: "read_file"})
	if got := toolSchemaDigest(reordered); got == first {
		t.Fatal("order-insensitive digest would miss a wire-affecting tool-order change (INV-68-1)")
	}
}

// TestPrefixIdentityToolSchemaDigestChangesOnAdmission pins that widening the
// registry through ScopedRegistryWithTail (the plan tools/05 admission path)
// changes the digest, which is what makes the W3 tool_admission reset event
// fire.
func TestPrefixIdentityToolSchemaDigestChangesOnAdmission(t *testing.T) {
	src := tools.NewRegistry()
	for _, name := range []string{"read_file", "grep", "glob"} {
		src.Register(fixedBodyTool{name: name})
	}
	opts := tools.ScopeOptions{Mode: tools.ScopeRoot, Allowlist: map[string]struct{}{"read_file": {}, "grep": {}}}
	core := tools.ScopedRegistry(src, opts)
	wider := tools.ScopedRegistryWithTail(src, opts, []string{"glob"})

	coreDigest := toolSchemaDigest(core)
	widerDigest := toolSchemaDigest(wider)
	if coreDigest == widerDigest {
		t.Fatalf("core vs core-plus-admitted produced the same digest %s", coreDigest)
	}
}

// TestPrefixIdentitySystemPromptDigestMatchesRenderedPrompt pins that the
// digest is the sha256 of exactly the system-prompt text that reaches the
// wire, including the empty-prompt case.
func TestPrefixIdentitySystemPromptDigestMatchesRenderedPrompt(t *testing.T) {
	prompt := "you are a test assistant\nwith two lines"
	if got := systemPromptDigest(prompt); got != sha256Hex(prompt) {
		t.Fatalf("digest = %s, want sha256 of the rendered prompt", got)
	}
	if got := systemPromptDigest(""); got != sha256Hex("") {
		t.Fatalf("empty-prompt digest = %s, want sha256(\"\")", got)
	}
}

func sha256Hex(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// TestPrefixIdentityTemperatureComparesByValueNotPointer pins AR-3: two
// identities with equal temperature values held at different *float64
// addresses compare equal, different values compare unequal, and
// HasTemperature distinguishes nil from set.
func TestPrefixIdentityTemperatureComparesByValueNotPointer(t *testing.T) {
	a, b := 0.7, 0.7
	idA := PrefixIdentity{ProviderName: "p", Model: "m", HasTemperature: true, Temperature: a}
	idB := PrefixIdentity{ProviderName: "p", Model: "m", HasTemperature: true, Temperature: b}
	if idA != idB {
		t.Fatal("equal temperature values at different addresses compare unequal (AR-3)")
	}
	idC := idA
	idC.Temperature = 0.8
	if idA == idC {
		t.Fatal("different temperature values compare equal")
	}
	idNil := PrefixIdentity{ProviderName: "p", Model: "m", Temperature: 0.7}
	if idNil == idA {
		t.Fatal("HasTemperature must distinguish a nil pointer from a set value")
	}
}
