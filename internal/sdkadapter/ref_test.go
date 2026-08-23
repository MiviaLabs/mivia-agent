package sdkadapter

import (
	"strings"
	"testing"
)

// TestParseCLIFormat parses a "ref:<kind>:<hex>" string and confirms the
// bridge extracts the canonical kind and digest. This is the format
// every CLI tool emits today, so a parse failure would silently break
// callers.
func TestParseCLIFormat(t *testing.T) {
	ref := "ref:output:" + strings.Repeat("a", 64)
	kind, digest, err := Parse(ref)
	if err != nil {
		t.Fatalf("Parse(%q): %v", ref, err)
	}
	if kind != "output" {
		t.Fatalf("Parse kind = %q, want %q", kind, "output")
	}
	if digest != strings.Repeat("a", 64) {
		t.Fatalf("Parse digest mismatch: %q", digest)
	}
}

// TestParseSDKFormat parses a "sha256:<hex>" string and confirms the
// bridge accepts it without modification. The SDK emits this format;
// consumers reading SDK-shaped references must round-trip without
// rejecting them.
func TestParseSDKFormat(t *testing.T) {
	hex := strings.Repeat("b", 64)
	ref := "sha256:" + hex
	kind, digest, err := Parse(ref)
	if err != nil {
		t.Fatalf("Parse(%q): %v", ref, err)
	}
	// The SDK format has no kind; the bridge returns an empty kind so
	// callers that need to distinguish formats can branch on the prefix.
	if kind != "" {
		t.Fatalf("Parse SDK kind = %q, want %q", kind, "")
	}
	if digest != hex {
		t.Fatalf("Parse SDK digest mismatch: %q", digest)
	}
}

// TestParseRejectsUnknown pins the bridge's fail-closed behaviour for
// anything that is neither a "ref:" CLI reference nor a "sha256:" SDK
// reference. A reference the bridge misread would propagate as a
// malformed downstream look-up, so refusing is the right default.
func TestParseRejectsUnknown(t *testing.T) {
	for _, ref := range []string{
		"",
		"not-a-ref",
		"ref:unknown:abcdef",
		"sha256:not-hex",
		"ref:output:not-hex",
	} {
		if _, _, err := Parse(ref); err == nil {
			t.Fatalf("Parse(%q) must error", ref)
		}
	}
}

// TestMintCLIFormat confirms that Mint with a CLI kind returns the CLI
// "ref:<kind>:<hex>" shape. The CLI side of the bridge is the one CLI
// tools mint today.
func TestMintCLIFormat(t *testing.T) {
	got := Mint("output", []byte("hello"))
	want := "ref:output:" + strings.Repeat("a", 64)
	// SHA-256 of "hello" is well known: 2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824
	const expectedDigest = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	want = "ref:output:" + expectedDigest
	if got != want {
		t.Fatalf("Mint(output, hello) = %q, want %q", got, want)
	}
}

// TestMintNoKindUsesSDKFormat confirms that Mint with an empty kind
// returns the SDK "sha256:<hex>" shape. Callers that want to emit
// SDK-shaped content references do not need a CLI kind.
func TestMintNoKindUsesSDKFormat(t *testing.T) {
	got := Mint("", []byte("hello"))
	want := "sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if got != want {
		t.Fatalf("Mint(no kind, hello) = %q, want %q", got, want)
	}
}

// TestMigrationWindowConstantPinned is the doc-test for the dual-parse
// forever decision. The constant exists to document when the CLI-only
// format goes away; it is read by humans, not by code, so a test that
// only observes its existence is sufficient.
func TestMigrationWindowConstantPinned(t *testing.T) {
	if migrationWindow == "" {
		t.Fatalf("migrationWindow constant must be set to a far-future date string")
	}
	// The date must parse; we do not check it is in the future because
	// the constant is documented in prose, not enforced at runtime.
	if !strings.HasPrefix(migrationWindow, "20") || len(migrationWindow) != len("2026-12-31") {
		t.Fatalf("migrationWindow %q must be a YYYY-MM-DD date string", migrationWindow)
	}
}
