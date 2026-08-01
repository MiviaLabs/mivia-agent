package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/redact"
)

// Recorded task output is arbitrary bytes, but page cursors are defined over
// model-visible UTF-8. Invalid sequences therefore have one deterministic
// replacement before redaction and paging.
func TestNormalizeLedgerContentReplacesInvalidUTF8(t *testing.T) {
	bin := string([]byte{0xff, 0xfe, 0xfd, 0xfc, 0xfb, 0xfa, 0xf9, 0xf8})
	got := normalizeLedgerContent([]byte(bin))
	if got == "" || !utf8.ValidString(got) {
		t.Fatalf("invalid content was not converted to a non-empty valid UTF-8 stream: %q", got)
	}
}

// A page edge landing inside a multi-byte rune must back off to the boundary.
func TestLedgerPageEndBacksOffMidRune(t *testing.T) {
	content := strings.Repeat("é", 10) // 2 bytes per rune
	if got := ledgerPageEnd(content, 0, 5); got != 4 {
		t.Fatalf("ledgerPageEnd = %d, want 4 (cut backed off out of the rune)", got)
	}
}

func TestLedgerPageEndLeavesShortContentAlone(t *testing.T) {
	if got := ledgerPageEnd("short", 0, 64); got != len("short") {
		t.Fatalf("ledgerPageEnd(short) = %d, want %d", got, len("short"))
	}
}

// Redaction must run before truncation. A length-anchored pattern - the common
// shape for secret detection, since key formats have fixed lengths - stops
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
