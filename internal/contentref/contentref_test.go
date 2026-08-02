package contentref

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func TestReferenceIsDeterministicAndCanonical(t *testing.T) {
	data := []byte("hello")
	sum := sha256.Sum256(data)
	want := "ref:output:" + hex.EncodeToString(sum[:])

	got := Reference(KindOutput, data)
	if got != want {
		t.Fatalf("Reference(output, %q) = %q, want %q", data, got, want)
	}

	if again := Reference(KindOutput, data); again != got {
		t.Fatalf("Reference is not deterministic: %q then %q", got, again)
	}

	if other := Reference(KindOutput, []byte("world")); other == got {
		t.Fatalf("different data produced the same reference %q", other)
	}

	if len(got) != 75 {
		t.Fatalf("len(%q) = %d, want 75", got, len(got))
	}

	digest := strings.TrimPrefix(got, "ref:output:")
	if len(digest) != 64 {
		t.Fatalf("digest %q has length %d, want 64", digest, len(digest))
	}
	for _, r := range digest {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
			t.Fatalf("digest %q contains non lowercase-hex rune %q", digest, r)
		}
	}
}

func TestReferenceEmptyDataYieldsEmptyRef(t *testing.T) {
	if got := Reference(KindOutput, nil); got != "" {
		t.Fatalf("Reference(output, nil) = %q, want empty", got)
	}
	if got := Reference(KindError, []byte{}); got != "" {
		t.Fatalf("Reference(error, []byte{}) = %q, want empty", got)
	}
}

// Minting a kind that ParseReference rejects would create an unparseable
// reference, so unknown kinds must yield no reference at all.
func TestReferenceUnknownKindYieldsEmptyRef(t *testing.T) {
	if got := Reference("sha256", []byte("x")); got != "" {
		t.Fatalf(`Reference("sha256", "x") = %q, want empty`, got)
	}
	if got := Reference("", []byte("x")); got != "" {
		t.Fatalf(`Reference("", "x") = %q, want empty`, got)
	}
}

func TestParseReferenceRoundTrips(t *testing.T) {
	for _, kind := range []string{KindOutput, KindError, KindMessage} {
		data := []byte("payload for " + kind)
		ref := Reference(kind, data)

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

func TestParseReferenceRejectsMalformed(t *testing.T) {
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
		{"uppercase hex", "ref:output:" + strings.Repeat("A", 64)},
		{"non hex", "ref:output:" + strings.Repeat("g", 64)},
		{"unknown kind", "ref:sha256:" + hex64},
		{"extra colon segment", "ref:output:" + hex64 + ":x"},
		{"leading space", " ref:output:" + hex64},
		{"trailing space", "ref:output:" + hex64 + " "},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, digest, err := Parse(tc.ref)
			if !errors.Is(err, ErrMalformed) {
				t.Fatalf("Parse(%q) error = %v, want ErrMalformed", tc.ref, err)
			}
			if kind != "" {
				t.Fatalf("Parse(%q) kind = %q, want empty", tc.ref, kind)
			}
			if digest != "" {
				t.Fatalf("Parse(%q) digest = %q, want empty", tc.ref, digest)
			}
		})
	}
}
