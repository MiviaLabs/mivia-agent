package clichat

// Chunk 3: the write/read loop. store_note mints ref:note: references and
// ledger_read resolves them with no production change: the existing reader
// already resolves any canonical kind. These tests pin that the loop is
// byte-exact and that the reader's error vocabulary (not_found vs malformed)
// covers the note kind too.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
)

// storeNoteViaTool runs store_note once and returns its response envelope.
func storeNoteViaTool(t *testing.T, repo ledger.LedgerRepository, content string) storeNoteResponse {
	t.Helper()
	tool := newDefaultStoreNoteTool(repo)
	out, err := tool.Execute(taskContext("run-read", "task-read"), json.RawMessage(`{"content":`+jsonQuote(content)+`}`))
	if err != nil {
		t.Fatal(err)
	}
	var response storeNoteResponse
	if err := json.Unmarshal([]byte(out), &response); err != nil {
		t.Fatalf("unmarshal %s: %v", out, err)
	}
	return response
}

func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// storeNoteResponse is the decoded store_note success envelope.
type storeNoteResponse struct {
	Ref   string `json:"ref"`
	Bytes int    `json:"bytes"`
	Error string `json:"error"`
}

// resolveRefViaLedgerRead runs ledger_read against ref and returns the
// decoded envelope.
func resolveRefViaLedgerRead(t *testing.T, repo ledger.LedgerRepository, ref string) ledgerReadResponse {
	t.Helper()
	tool := &ledgerReadTool{repo: repo}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"ref":`+jsonQuote(ref)+`}`))
	if err != nil {
		t.Fatal(err)
	}
	var response ledgerReadResponse
	if err := json.Unmarshal([]byte(out), &response); err != nil {
		t.Fatalf("unmarshal %s: %v", out, err)
	}
	return response
}

// TestStoreNoteThenLedgerReadRoundTrip stores a note through the tool and
// resolves it through the reader: kind "note", byte-identical content.
func TestStoreNoteThenLedgerReadRoundTrip(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	const body = "overflow detail: table of failures\nrow 2\n"
	written := storeNoteViaTool(t, repo, body)
	if written.Ref == "" {
		t.Fatalf("store_note returned no ref: %+v", written)
	}
	read := resolveRefViaLedgerRead(t, repo, written.Ref)
	if read.Status != "ok" {
		t.Fatalf("ledger_read status = %q, want ok", read.Status)
	}
	if read.Kind != ledger.RefKindNote {
		t.Fatalf("ledger_read kind = %q, want note", read.Kind)
	}
	if read.Content != body {
		t.Fatalf("resolved content %q is not byte-identical to the stored note", read.Content)
	}
}

// TestLedgerReadUnknownNoteRefIsNotFound pins the dead-pointer vocabulary: a
// well-formed ref:note: digest that was never stored is not_found, not
// malformed.
func TestLedgerReadUnknownNoteRefIsNotFound(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	// A canonical digest over bytes nobody stored.
	unknown := ledger.Reference(ledger.RefKindNote, []byte("never stored through this repo"))
	if unknown == "" {
		t.Fatal("minter returned empty for a known kind")
	}
	read := resolveRefViaLedgerRead(t, repo, unknown)
	if read.Status != "not_found" {
		t.Fatalf("status = %q, want not_found (envelope %+v)", read.Status, read)
	}
}

// TestLedgerReadMalformedNoteRefIsMalformed pins the malformed answer for a
// ref:note: handle with a non-hex digest.
func TestLedgerReadMalformedNoteRefIsMalformed(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	read := resolveRefViaLedgerRead(t, repo, "ref:note:zz")
	if read.Status == "not_found" || read.Content != "" {
		t.Fatalf("a malformed ref must never read as a dead pointer: %+v", read)
	}
	if !strings.Contains(read.Error, "malformed") {
		t.Fatalf("error = %q, want the malformed-reference answer", read.Error)
	}
}
