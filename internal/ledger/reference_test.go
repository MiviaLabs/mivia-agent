package ledger

import (
	"errors"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
)

// The ledger's reference API is a delegation to the one canonical
// minter in internal/sdkadapter, not a second implementation. These
// tests pin the delegation: if someone reimplements minting or parsing
// here, the formats can drift apart again, which is the defect class
// the invariant exists to prevent.

func TestLedgerReferenceDelegatesToCanonicalMinter(t *testing.T) {
	for _, data := range [][]byte{[]byte("hello"), []byte("payload"), []byte("{}")} {
		for _, kind := range []string{RefKindOutput, RefKindError} {
			want := sdkadapter.Mint(kind, data)
			if got := Reference(kind, data); got != want {
				t.Fatalf("Reference(%q, %q) = %q, want %q", kind, data, got, want)
			}
		}
	}
}

func TestLedgerReferenceKindsMatchCanonicalKinds(t *testing.T) {
	if RefKindOutput != sdkadapter.KindOutput {
		t.Fatalf("RefKindOutput = %q, want %q", RefKindOutput, sdkadapter.KindOutput)
	}
	if RefKindError != sdkadapter.KindError {
		t.Fatalf("RefKindError = %q, want %q", RefKindError, sdkadapter.KindError)
	}
}

// ErrMalformedReference must be the same sentinel the minter returns,
// or errors.Is checks written against either name would silently
// disagree.
func TestLedgerMalformedReferenceIsCanonicalSentinel(t *testing.T) {
	if !errors.Is(ErrMalformedReference, sdkadapter.ErrMalformedReference) {
		t.Fatal("ErrMalformedReference is not sdkadapter.ErrMalformedReference")
	}
	_, _, err := ParseReference("ref:sha256:" + strings.Repeat("a", 64))
	if !errors.Is(err, ErrMalformedReference) {
		t.Fatalf("ParseReference error = %v, want ErrMalformedReference", err)
	}
}

func TestLedgerParseReferenceRoundTripsCanonicalRef(t *testing.T) {
	ref := Reference(RefKindOutput, []byte("payload"))
	kind, digest, err := ParseReference(ref)
	if err != nil {
		t.Fatalf("ParseReference(%q) error = %v, want nil", ref, err)
	}
	if kind != RefKindOutput {
		t.Fatalf("kind = %q, want %q", kind, RefKindOutput)
	}
	if len(digest) != 64 {
		t.Fatalf("digest %q has length %d, want 64", digest, len(digest))
	}
}
