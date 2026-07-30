package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/redact"
)

// Recorded task output is arbitrary bytes, not guaranteed valid UTF-8. An
// unbounded walk back to a valid rune boundary finds no valid prefix in binary
// content and returns nothing, so capping the size would silently erase the
// whole payload instead of trimming it.
func TestTruncateUTF8TrimsRatherThanErasesNonUTF8(t *testing.T) {
	bin := string([]byte{0xff, 0xfe, 0xfd, 0xfc, 0xfb, 0xfa, 0xf9, 0xf8})
	got, truncated := truncateUTF8(bin, 4)
	if !truncated {
		t.Fatal("expected truncated=true")
	}
	if len(got) == 0 {
		t.Fatal("non-UTF-8 content was erased entirely instead of trimmed")
	}
	if len(got) > 4 {
		t.Fatalf("len(got) = %d, exceeds cap 4", len(got))
	}
}

// A cut landing inside a multi-byte rune must back off to the boundary rather
// than emit a broken rune.
func TestTruncateUTF8BacksOffMidRune(t *testing.T) {
	got, truncated := truncateUTF8(strings.Repeat("é", 10), 5) // 2 bytes per rune
	if !truncated {
		t.Fatal("expected truncated=true")
	}
	if len(got) != 4 {
		t.Fatalf("len(got) = %d, want 4 (cut backed off out of the rune)", len(got))
	}
}

func TestTruncateUTF8LeavesShortContentAlone(t *testing.T) {
	got, truncated := truncateUTF8("short", 64)
	if truncated || got != "short" {
		t.Fatalf("truncateUTF8(short) = (%q, %v), want (short, false)", got, truncated)
	}
}

// Redaction must run before truncation. A length-anchored pattern — the common
// shape for secret detection, since key formats have fixed lengths — stops
// matching once the cut has shortened the secret, so truncating first hands the
// model the surviving prefix verbatim. The quantifier below is what makes this
// test meaningful: an open-ended `+` would match a bisected secret too and the
// assertion would hold under either order.
func TestLedgerReadRedactsBeforeTruncating(t *testing.T) {
	policy, err := redact.Compile([]string{`sk-live-[A-Za-z0-9]{26}`}, nil, "[redacted]")
	if err != nil {
		t.Fatalf("compile policy: %v", err)
	}
	previous := redact.Current()
	redact.SetPolicy(policy)
	t.Cleanup(func() { redact.SetPolicy(previous) })

	const secret = "sk-live-ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	body := []byte("leading padding " + secret + " trailing")
	ref := ledger.Reference(ledger.RefKindOutput, body)

	repo := ledger.NewMemoryLedgerRepository()
	if err := repo.StoreContent(t.Context(), ref, body); err != nil {
		t.Fatalf("StoreContent: %v", err)
	}

	// A cap that lands inside the secret had the old order emitted "sk-live-ABC…".
	tool := &ledgerReadTool{repo: repo, maxBytes: len("leading padding ") + 12}
	out, err := tool.Execute(t.Context(), json.RawMessage(`{"ref":"`+ref+`"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var got struct {
		Status  string `json:"status"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	if got.Status != "ok" {
		t.Fatalf("status = %q, want ok (%s)", got.Status, out)
	}
	if strings.Contains(got.Content, "sk-live-") {
		t.Fatalf("truncated content leaked a secret prefix: %q", got.Content)
	}
}
