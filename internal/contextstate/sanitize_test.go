package contextstate

import (
	"context"
	"errors"
	"testing"
)

func TestSanitizeSourcePayloadConfiguredAndHashOnly(t *testing.T) {
	principal, err := NewPrincipal("workspace", "session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("bounded source value")

	hashOnly, err := SanitizeSourcePayload(context.Background(), principal, data, RedactionPolicy{})
	if err != nil {
		t.Fatalf("hash-only sanitize: %v", err)
	}
	if !hashOnly.HashOnly || hashOnly.Dereferenceable || len(hashOnly.Bytes) != 0 {
		t.Fatalf("unconfigured payload = %+v, want non-dereferenceable hash-only", hashOnly)
	}

	configured := RedactionPolicy{Configured: true, Patterns: []string{"blocked-value"}}
	if _, err := SanitizeSourcePayload(context.Background(), principal, []byte("contains blocked-value"), configured); !errors.Is(err, ErrInvalidDTO) {
		t.Fatalf("configured classifier error = %v, want ErrInvalidDTO", err)
	}
	configured.Patterns = []string{"not-present"}
	full, err := SanitizeSourcePayload(context.Background(), principal, data, configured)
	if err != nil {
		t.Fatalf("configured sanitize: %v", err)
	}
	if full.HashOnly || !full.Dereferenceable || string(full.Bytes) != string(data) {
		t.Fatalf("configured payload = %+v, want bounded bytes", full)
	}
	if _, err := SanitizeSourcePayload(context.Background(), principal, []byte{0xff}, configured); !errors.Is(err, ErrInvalidDTO) {
		t.Fatalf("malformed UTF-8 error = %v, want ErrInvalidDTO", err)
	}
}
