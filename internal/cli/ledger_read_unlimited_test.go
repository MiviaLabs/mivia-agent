package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
)

// TestLedgerReadDefaultPagesLargeContent verifies that the default response is
// a finite page, leaving a continuation cursor rather than relying on an outer
// result cap to cut a whole JSON envelope.
func TestLedgerReadDefaultPagesLargeContent(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()

	largeContent := strings.Repeat("A", 100*1024)
	ref := storeContentHelper(t, repo, []byte(largeContent))

	// Construct with zero maxBytes (the default).
	tool := &ledgerReadTool{repo: repo}

	args, err := json.Marshal(map[string]string{"ref": ref})
	if err != nil {
		t.Fatal(err)
	}
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	var response struct {
		Status    string `json:"status"`
		Ref       string `json:"ref"`
		Kind      string `json:"kind"`
		Bytes     int    `json:"bytes"`
		Truncated bool   `json:"truncated"`
		Content   string `json:"content"`
	}
	if err := json.Unmarshal([]byte(out), &response); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if response.Status != "ok" {
		t.Fatalf("expected status ok, got: %s", out)
	}
	if len(response.Content) == 0 || len(response.Content) >= len(largeContent) {
		t.Fatalf("content length = %d, want a non-empty bounded page below %d", len(response.Content), len(largeContent))
	}
	if !response.Truncated {
		t.Fatal("expected truncated=true for a bounded default page")
	}
}

// TestLedgerReadContentIsIntact stores content with a known structure (a JSON
// object >2 KB) and verifies the returned content round-trips exactly.
func TestLedgerReadContentIsIntact(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()

	// Build a JSON object larger than 2 KB with a known, verifiable structure.
	parts := make([]string, 50)
	for i := range parts {
		parts[i] = fmt.Sprintf(`"key_%d":"value_%0128d"`, i, i)
	}
	original := "{" + strings.Join(parts, ",") + "}"
	if len(original) <= 2048 {
		t.Fatalf("test content is only %d bytes, need >2048", len(original))
	}

	ref := storeContentHelper(t, repo, []byte(original))

	tool := &ledgerReadTool{repo: repo} // maxBytes=0 (default = unlimited)

	args, err := json.Marshal(map[string]string{"ref": ref})
	if err != nil {
		t.Fatal(err)
	}
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	var response ledgerReadResponse
	if err := json.Unmarshal([]byte(out), &response); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if response.Status != "ok" {
		t.Fatalf("expected status ok, got: %s", out)
	}
	if response.Content != original {
		// Byte-for-byte comparison: any difference means content corruption.
		t.Fatalf("content does not match original (got %d bytes, want %d)",
			len(response.Content), len(original))
	}

	// Also verify the JSON structure is parseable and contains expected keys.
	var decoded map[string]string
	if err := json.Unmarshal([]byte(response.Content), &decoded); err != nil {
		t.Fatalf("returned content is not valid JSON: %v", err)
	}
	if decoded["key_0"] != fmt.Sprintf("value_%0128d", 0) {
		t.Fatalf("decoded key_0 = %q, unexpected", decoded["key_0"])
	}
	if decoded["key_49"] != fmt.Sprintf("value_%0128d", 49) {
		t.Fatalf("decoded key_49 = %q, unexpected", decoded["key_49"])
	}
	if len(decoded) != 50 {
		t.Fatalf("decoded map has %d entries, want 50", len(decoded))
	}
}

// storeContentHelper stores data under its canonical reference and returns it.
// This is a copy of the unexported storeLedgerContent in ledger_tools_test.go,
// which is not accessible from a separate _test.go file in the same package
// only when both define the same function name (linker collision). We use a
// distinct name to avoid conflicts.
func storeContentHelper(t *testing.T, repo ledger.LedgerRepository, data []byte) string {
	t.Helper()
	ref := ledger.Reference(ledger.RefKindOutput, data)
	if ref == "" {
		t.Fatalf("canonical reference for %d bytes was empty", len(data))
	}
	if err := repo.StoreContent(context.Background(), ref, data); err != nil {
		t.Fatal(err)
	}
	return ref
}
