package sdkadapter

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	sdkref "github.com/MiviaLabs/mivia-ai-sdk/contextstate"
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

// TestKindConstantsMatchVocabulary pins the CLI kind vocabulary so a
// silent rename cannot regress the bridge. Every CLI tool that mints
// or recognises a "ref:<kind>:<digest>" string imports these
// constants; the three documented kinds are the only ones accepted by
// Parse and Mint.
func TestKindConstantsMatchVocabulary(t *testing.T) {
	if KindOutput != "output" {
		t.Fatalf("KindOutput = %q, want %q", KindOutput, "output")
	}
	if KindError != "error" {
		t.Fatalf("KindError = %q, want %q", KindError, "error")
	}
	if KindMessage != "message" {
		t.Fatalf("KindMessage = %q, want %q", KindMessage, "message")
	}
	// KindToolCalls tags a subagent's recorded tool-call step trace
	// (internal/ledger's TaskSnapshot.ToolCallsRef), the fourth kind of
	// content-addressed reference the bridge mints and recognises.
	if KindToolCalls != "tool_calls" {
		t.Fatalf("KindToolCalls = %q, want %q", KindToolCalls, "tool_calls")
	}
}

// TestMintCLIEmptyDataYieldsEmptyRef pins the empty-data contract the
// remainder spool relies on: a mint over an empty body must yield "",
// not a one-digest reference to a key nothing stored. INV-AG-10 keeps
// the model from ever seeing that phantom ref.
func TestMintCLIEmptyDataYieldsEmptyRef(t *testing.T) {
	if got := Mint(KindOutput, nil); got != "" {
		t.Fatalf("Mint(output, nil) = %q, want empty", got)
	}
	if got := Mint(KindError, []byte{}); got != "" {
		t.Fatalf("Mint(error, []byte{}) = %q, want empty", got)
	}
}

// TestMintUnknownKindYieldsEmptyRef refuses to mint a kind the parser
// would reject. A reference that mints but does not parse is the
// regression class the invariant exists to prevent.
func TestMintUnknownKindYieldsEmptyRef(t *testing.T) {
	if got := Mint("sha256", []byte("x")); got != "" {
		t.Fatalf(`Mint("sha256", "x") = %q, want empty`, got)
	}
	if got := Mint("", []byte("x")); got == "" {
		t.Fatalf(`Mint("", "x") = empty, want the SDK sha256 shape`)
	}
}

// TestParseAndMintRoundTripCLI walks every known kind and asserts the
// Mint -> Parse round trip recovers the (kind, digest) pair byte
// identically. This is the bridge's correctness contract: the same
// data the model handed to Mint must round-trip through Parse.
func TestParseAndMintRoundTripCLI(t *testing.T) {
	for _, kind := range []string{KindOutput, KindError, KindMessage, KindToolCalls} {
		data := []byte("payload for " + kind)
		ref := Mint(kind, data)
		if ref == "" {
			t.Fatalf("Mint(%s, data) returned empty", kind)
		}
		gotKind, gotDigest, err := Parse(ref)
		if err != nil {
			t.Fatalf("Parse(%q) error = %v, want nil", ref, err)
		}
		if gotKind != kind {
			t.Fatalf("Parse(%q) kind = %q, want %q", ref, gotKind, kind)
		}
		sum := sha256.Sum256(data)
		wantDigest := hex.EncodeToString(sum[:])
		if gotDigest != wantDigest {
			t.Fatalf("Parse(%q) digest = %q, want %q", ref, gotDigest, wantDigest)
		}
		if len(gotDigest) != 64 {
			t.Fatalf("digest %q has length %d, want 64", gotDigest, len(gotDigest))
		}
	}
}

// TestParseRejectsMalformedCLI pins every malformed CLI shape the
// bridge must refuse. Regression lock (finding R0-1): a historical
// minter truncated the digest to 8 bytes, which encodes as exactly 16
// hex chars, so every reference it minted pointed at a key nothing had
// stored. This exact width must be rejected; the 63/65-hex cases above
// do not name it.
func TestParseRejectsMalformedCLI(t *testing.T) {
	hex64 := strings.Repeat("a", 64)
	cases := []struct {
		name string
		ref  string
	}{
		{"empty", ""},
		{"not a ref", "hello"},
		{"missing digest", "ref:output"},
		{"empty digest", "ref:output:"},
		{"short digest", "ref:output:abc"},
		{"63 hex chars", "ref:output:" + strings.Repeat("a", 63)},
		{"65 hex chars", "ref:output:" + strings.Repeat("a", 65)},
		{"historical truncated digest (8 bytes = 16 hex chars)", "ref:output:" + strings.Repeat("a", 16)},
		{"oversized digest (4096 hex chars)", "ref:output:" + strings.Repeat("a", 4096)},
		{"uppercase hex", "ref:output:" + strings.Repeat("A", 64)},
		{"non hex", "ref:output:" + strings.Repeat("g", 64)},
		{"unknown kind", "ref:sha256:" + hex64},
		{"extra colon segment", "ref:output:" + hex64 + ":x"},
		{"leading space", " ref:output:" + hex64},
		{"trailing space", "ref:output:" + hex64 + " "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := Parse(tc.ref)
			if !errors.Is(err, ErrMalformedReference) {
				t.Fatalf("Parse(%q) error = %v, want ErrMalformedReference", tc.ref, err)
			}
		})
	}
}

// TestMintEmitsFullSHA256Digest pins the minting contract behaviorally:
// every known kind mints exactly "ref:<kind>:" + hex(sha256(data)) with
// a 64-character lowercase-hex digest. This fails for any minter that
// truncates the digest to 8 bytes (16 hex chars) - the confirmed
// historical defect that made every output reference point at a key
// nothing had stored.
func TestMintEmitsFullSHA256Digest(t *testing.T) {
	payloads := map[string][]byte{
		"ascii":            []byte("hello world"),
		"multi-byte UTF-8": []byte("héllo wörld — 你好"),
		"binary":           {0x00, 0x01, 0xff, 0xfe, 0x80, 0x7f},
	}
	for _, kind := range []string{KindOutput, KindError, KindMessage, KindToolCalls} {
		for name, data := range payloads {
			sum := sha256.Sum256(data)
			want := "ref:" + kind + ":" + hex.EncodeToString(sum[:])
			got := Mint(kind, data)
			if got != want {
				t.Fatalf("Mint(%s, %s payload) = %q, want %q", kind, name, got, want)
			}
			digest := strings.TrimPrefix(got, "ref:"+kind+":")
			if len(digest) != 64 {
				t.Fatalf("Mint(%s, %s payload) digest %q has length %d, want 64", kind, name, digest, len(digest))
			}
			for _, r := range digest {
				if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
					t.Fatalf("Mint(%s, %s payload) digest %q contains non lowercase-hex rune %q", kind, name, digest, r)
				}
			}
		}
	}
}

// FuzzParseNeverPanics asserts Parse never panics on any input.
// References cross the trust boundary (they arrive via cli/read_output,
// agentmsg validateRef, and ledger ParseReference re-exports), so a
// panic on hostile input would be a reachable defect regardless of
// whether the shape is valid.
func FuzzParseNeverPanics(f *testing.F) {
	seedCorpus(f)
	f.Fuzz(func(t *testing.T, s string) {
		_, _, _ = Parse(s) // must never panic
	})
}

// FuzzParseRejectsNonCanonical asserts Parse is sound and complete:
//   - (soundness) when Parse accepts s, s must have exactly the canonical
//     shape "ref:<known kind>:<64 lowercase hex>" OR "sha256:<64 hex>";
//   - (completeness) when s has one of those canonical shapes, Parse must
//     accept it and return the split kind and digest.
//
// The CLI half preserves the contentref invariant; the SDK half keeps
// its dual-format promise during the migration window.
func FuzzParseRejectsNonCanonical(f *testing.F) {
	seedCorpus(f)
	f.Fuzz(func(t *testing.T, s string) {
		kind, digest, err := Parse(s)
		canonicalCLI := isCanonicalCLI(s)
		canonicalSDK := sdkref.IsRef(s)
		switch {
		case canonicalCLI && err != nil:
			t.Fatalf("Parse rejected canonical CLI ref %q: %v", s, err)
		case canonicalSDK && err != nil:
			t.Fatalf("Parse rejected canonical SDK ref %q: %v", s, err)
		case err == nil && !canonicalCLI && !canonicalSDK:
			t.Fatalf("Parse accepted non-canonical ref %q", s)
		case err == nil && canonicalCLI:
			parts := strings.Split(s, ":")
			if kind != parts[1] || digest != parts[2] {
				t.Fatalf("Parse(%q) = (%q, %q), want (%q, %q)", s, kind, digest, parts[1], parts[2])
			}
		}
	})
}

// isCanonicalCLI reports whether s is exactly "ref:<known kind>:<64
// lowercase hex>". The same predicate the deleted contentref package
// applied, kept here so the fuzz oracle is a closed check rather than
// a copy of the parse path.
func isCanonicalCLI(s string) bool {
	parts := strings.Split(s, ":")
	if len(parts) != 3 || parts[0] != "ref" || !knownKind(parts[1]) {
		return false
	}
	return isLowerHexDigest(parts[2])
}

// seedCorpus primes both targets with the structured-input classes the
// regression tests name: empty, truncated-to-16-hex, 63/65-hex,
// oversized, uppercase hex, non-hex, unknown kind, surrounding
// whitespace, an extra colon segment, and canonical refs for every
// known kind plus the SDK shape.
func seedCorpus(f *testing.F) {
	hex64 := strings.Repeat("a", 64)
	f.Add("")
	f.Add("ref:output:")
	f.Add("ref:output:" + strings.Repeat("a", 16)) // historical 8-byte truncation
	f.Add("ref:output:" + strings.Repeat("a", 63))
	f.Add("ref:output:" + strings.Repeat("a", 65))
	f.Add("ref:output:" + strings.Repeat("a", 4096))
	f.Add("ref:output:" + strings.Repeat("A", 64)) // uppercase hex
	f.Add("ref:output:" + strings.Repeat("g", 64)) // non-hex
	f.Add("ref:sha256:" + hex64)                   // unknown kind
	f.Add(" ref:output:" + hex64)                  // leading whitespace
	f.Add("ref:output:" + hex64 + " ")             // trailing whitespace
	f.Add("ref:output:" + hex64 + ":x")            // extra colon segment
	f.Add("ref:output:" + hex64)                   // canonical output
	f.Add("ref:error:" + hex64)                    // canonical error
	f.Add("ref:message:" + hex64)                  // canonical message
	f.Add("sha256:" + hex64)                       // canonical SDK
	f.Add("sha256:not-hex")                        // malformed SDK
}
