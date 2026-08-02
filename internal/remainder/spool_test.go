package remainder_test

import (
	"context"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contentref"
	"github.com/MiviaLabs/mivia-agent/internal/remainder"
)

func TestSpoolRoundTripAndVisibility(t *testing.T) {
	store := remainder.NewMemoryStore()
	spool := remainder.NewSpool(store)
	const principal = "session-a"
	body := []byte(strings.Repeat("remainder-body-", 64))

	ref := spool.Spool(context.Background(), principal, body)
	if ref == "" {
		t.Fatal("expected non-empty ref after successful spool")
	}
	if _, _, err := contentref.Parse(ref); err != nil {
		t.Fatalf("minted ref not canonical: %v", err)
	}
	if !strings.HasPrefix(ref, "ref:output:") {
		t.Fatalf("ref = %q, want ref:output:…", ref)
	}

	got, err := spool.Load(context.Background(), principal, ref)
	if err != nil {
		t.Fatalf("owner load: %v", err)
	}
	if string(got) != string(body) {
		t.Fatalf("loaded body mismatch: got %d bytes want %d", len(got), len(body))
	}

	_, err = spool.Load(context.Background(), "session-b", ref)
	if err != remainder.ErrDenied {
		t.Fatalf("cross-principal load err = %v, want ErrDenied", err)
	}
}

func TestSpoolFailureOmitsRef(t *testing.T) {
	spool := remainder.NewSpool(remainder.FailingStore{})
	ref := spool.Spool(context.Background(), "session-a", []byte("payload-large-enough"))
	if ref != "" {
		t.Fatalf("failed store minted ref %q", ref)
	}
}

func TestSpoolExpiredDistinctFromNotFound(t *testing.T) {
	store := remainder.NewMemoryStore()
	spool := remainder.NewSpool(store)
	body := []byte("expires-soon")
	ref := spool.Spool(context.Background(), "session-a", body)
	if ref == "" {
		t.Fatal("empty ref")
	}
	spool.MarkExpired(ref)
	_, err := spool.Load(context.Background(), "session-a", ref)
	if err != remainder.ErrExpired {
		t.Fatalf("after MarkExpired: %v, want ErrExpired", err)
	}

	_, err = spool.Load(context.Background(), "session-a", "ref:output:"+strings.Repeat("0", 64))
	if err != remainder.ErrNotFound {
		t.Fatalf("unknown ref: %v, want ErrNotFound", err)
	}
}

func TestCapWithSpoolNoticeIncludesRef(t *testing.T) {
	store := remainder.NewMemoryStore()
	spool := remainder.NewSpool(store)
	body := strings.Repeat("Z", 500)
	// Budget must clear the full ref notice (~110 bytes) plus some body.
	out, truncated := remainder.CapWithSpool(spool, "session-a", body, 256)
	if !truncated {
		t.Fatal("expected truncation")
	}
	if len(out) > 256 {
		t.Fatalf("out len %d > budget 256", len(out))
	}
	if !strings.Contains(out, "truncated: kept ") {
		t.Fatalf("missing kept/total notice: %q", out)
	}
	if !strings.Contains(out, "use read_output") {
		t.Fatalf("missing read_output guidance: %q", out)
	}
	if !strings.Contains(out, "ref:output:") {
		t.Fatalf("successful spool omitted ref: %q", out)
	}
	idx := strings.Index(out, "ref:output:")
	end := idx
	for end < len(out) && out[end] != ',' && out[end] != ')' {
		end++
	}
	ref := out[idx:end]
	got, err := spool.Load(context.Background(), "session-a", ref)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Fatalf("spooled body mismatch")
	}
}

func TestCapWithSpoolFailedStoreOmitsRef(t *testing.T) {
	spool := remainder.NewSpool(remainder.FailingStore{})
	body := strings.Repeat("Y", 200)
	out, truncated := remainder.CapWithSpool(spool, "session-a", body, 80)
	if !truncated {
		t.Fatal("expected truncation")
	}
	if strings.Contains(out, "ref:output:") {
		t.Fatalf("failed spool invented a ref: %q", out)
	}
	if !strings.Contains(out, "truncated: kept ") {
		t.Fatalf("missing notice: %q", out)
	}
}
