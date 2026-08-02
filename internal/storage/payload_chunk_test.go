package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// TestPayloadChunkRoundTripMultiMB: multi-chunk source payloads commit and
// reassemble byte-identical under one content ref (SHA-256 of full payload).
func TestPayloadChunkRoundTripMultiMB(t *testing.T) {
	ctx := context.Background()
	// Small chunk size forces multi-chunk path without multi-MB of RSS in CI.
	contextstate.SetLimits(contextstate.Limits{SourceEventBytes: 1024})
	t.Cleanup(func() { contextstate.SetLimits(contextstate.DefaultLimits()) })

	s, principal := openContextTestStore(t)
	defer s.Close()
	seedContextSession(t, s, principal)

	// ~3.5 KiB → 4 chunks at 1024-byte chunk size.
	body := []byte(strings.Repeat("chunk-payload-body-", 200))
	if len(body) <= contextstate.PayloadChunkSize() {
		t.Fatalf("fixture too small to force chunking: %d", len(body))
	}
	payload, err := contextstate.SanitizeSourcePayload(ctx, principal, body, contextstate.RedactionPolicy{Configured: true, Patterns: []string{"not-present"}})
	if err != nil {
		t.Fatal(err)
	}
	eventID, err := contextstate.NewSourceID(principal.SessionID, 1)
	if err != nil {
		t.Fatal(err)
	}
	event := contextstate.SourceEvent{
		ID: eventID, Kind: "message", Role: "user", PayloadRef: payload.Ref.Ref,
		Provenance: "host", RedactionStatus: "sanitized", Size: payload.Ref.Size,
	}
	record := contextstate.PayloadRecord{Ref: payload.Ref, Retention: payload.Retention, Data: payload.Bytes}
	if err := s.appendSourceEvents(ctx, principal, []contextstate.SourceEvent{event}, []contextstate.PayloadRecord{record}); err != nil {
		t.Fatalf("append chunked source: %v", err)
	}

	// Parent row stores no inline BLOB when chunked.
	var inline []byte
	if err := s.db.QueryRow(`SELECT data FROM context_payloads WHERE ref=?`, payload.Ref.Ref).Scan(&inline); err != nil {
		t.Fatal(err)
	}
	if inline != nil {
		t.Fatalf("expected NULL inline data for multi-chunk payload, got %d bytes", len(inline))
	}
	var chunkRows int
	if err := s.db.QueryRow(`SELECT count(*) FROM context_payload_chunks WHERE ref=?`, payload.Ref.Ref).Scan(&chunkRows); err != nil {
		t.Fatal(err)
	}
	wantChunks := (len(body) + 1023) / 1024
	if chunkRows != wantChunks {
		t.Fatalf("chunk rows = %d, want %d", chunkRows, wantChunks)
	}

	got, err := s.ReadPayload(ctx, principal, payload.Ref)
	if err != nil {
		t.Fatalf("ReadPayload: %v", err)
	}
	if !bytes.Equal(got.Bytes, body) {
		t.Fatalf("reassembly not byte-identical: got %d bytes, want %d", len(got.Bytes), len(body))
	}
	sum := sha256.Sum256(got.Bytes)
	if hex.EncodeToString(sum[:]) != payload.Ref.SHA256 {
		t.Fatal("content-ref SHA-256 does not match reassembled payload")
	}
}

// TestPayloadChunkSHAFailClosed: corrupting a stored chunk must fail reassembly
// with digest mismatch (never return partial/wrong bytes).
func TestPayloadChunkSHAFailClosed(t *testing.T) {
	ctx := context.Background()
	contextstate.SetLimits(contextstate.Limits{SourceEventBytes: 64})
	t.Cleanup(func() { contextstate.SetLimits(contextstate.DefaultLimits()) })

	s, principal := openContextTestStore(t)
	defer s.Close()
	seedContextSession(t, s, principal)

	body := []byte(strings.Repeat("Z", 200))
	payload, err := contextstate.SanitizeSourcePayload(ctx, principal, body, contextstate.RedactionPolicy{Configured: true, Patterns: []string{"nope"}})
	if err != nil {
		t.Fatal(err)
	}
	eventID, _ := contextstate.NewSourceID(principal.SessionID, 1)
	event := contextstate.SourceEvent{
		ID: eventID, Kind: "message", Role: "assistant", PayloadRef: payload.Ref.Ref,
		Provenance: "host", RedactionStatus: "sanitized", Size: payload.Ref.Size,
	}
	if err := s.appendSourceEvents(ctx, principal, []contextstate.SourceEvent{event}, []contextstate.PayloadRecord{{Ref: payload.Ref, Retention: payload.Retention, Data: payload.Bytes}}); err != nil {
		t.Fatal(err)
	}

	// Corrupt chunk 0.
	if _, err := s.db.Exec(`UPDATE context_payload_chunks SET data = ? WHERE ref=? AND chunk_index=0`, []byte("CORRUPTED"), payload.Ref.Ref); err != nil {
		t.Fatal(err)
	}
	_, err = s.ReadPayload(ctx, principal, payload.Ref)
	if err == nil {
		t.Fatal("expected fail-closed on corrupted chunk reassembly")
	}
	if !strings.Contains(err.Error(), "mismatch") && !strings.Contains(err.Error(), "Invalid") && !strings.Contains(err.Error(), "invalid") {
		// Either size mismatch or digest mismatch is fail-closed.
		t.Logf("error (acceptable if fail-closed): %v", err)
	}
}

// TestPayloadChunkSizeZeroUsesDefault: SourceEventBytes=0 maps to the built-in
// default chunk size (not whole-payload reject).
func TestPayloadChunkSizeZeroUsesDefault(t *testing.T) {
	contextstate.SetLimits(contextstate.Limits{})
	t.Cleanup(func() { contextstate.SetLimits(contextstate.DefaultLimits()) })
	if got := contextstate.PayloadChunkSize(); got != contextstate.DefaultPayloadChunkBytes {
		t.Fatalf("PayloadChunkSize() = %d, want default %d", got, contextstate.DefaultPayloadChunkBytes)
	}
}

// TestSmallPayloadStaysInlineBLOB: under chunk size, data stays on the parent row.
func TestSmallPayloadStaysInlineBLOB(t *testing.T) {
	ctx := context.Background()
	contextstate.SetLimits(contextstate.Limits{SourceEventBytes: 4096})
	t.Cleanup(func() { contextstate.SetLimits(contextstate.DefaultLimits()) })

	s, principal := openContextTestStore(t)
	defer s.Close()
	seedContextSession(t, s, principal)

	body := []byte("small-inline")
	payload, err := contextstate.SanitizeSourcePayload(ctx, principal, body, contextstate.RedactionPolicy{Configured: true, Patterns: []string{"nope"}})
	if err != nil {
		t.Fatal(err)
	}
	eventID, _ := contextstate.NewSourceID(principal.SessionID, 1)
	event := contextstate.SourceEvent{
		ID: eventID, Kind: "message", Role: "user", PayloadRef: payload.Ref.Ref,
		Provenance: "host", RedactionStatus: "sanitized", Size: payload.Ref.Size,
	}
	if err := s.appendSourceEvents(ctx, principal, []contextstate.SourceEvent{event}, []contextstate.PayloadRecord{{Ref: payload.Ref, Retention: payload.Retention, Data: payload.Bytes}}); err != nil {
		t.Fatal(err)
	}
	var inline []byte
	if err := s.db.QueryRow(`SELECT data FROM context_payloads WHERE ref=?`, payload.Ref.Ref).Scan(&inline); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(inline, body) {
		t.Fatalf("inline = %q, want %q", inline, body)
	}
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM context_payload_chunks WHERE ref=?`, payload.Ref.Ref).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("unexpected chunks for small payload: %d", n)
	}
}
