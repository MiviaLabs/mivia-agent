package storage

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

func TestDeleteExportAuditAndRevocation(t *testing.T) {
	ctx := context.Background()
	s, principal := openContextTestStore(t)
	defer s.Close()
	seedContextSession(t, s, principal)
	payload, event := contextSourceFixture(t, principal, "delete-safe")
	if err := s.appendSourceEvents(ctx, principal, []contextstate.SourceEvent{event}, []contextstate.PayloadRecord{{Ref: payload.Ref, Retention: payload.Retention, Data: payload.Bytes}}); err != nil {
		t.Fatal(err)
	}
	first, err := s.DeleteSession(ctx, principal, principal.SessionID)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if first.RevokedRefs != 1 || first.AuditID == "" || first.TombstoneRevision.Session != 1 {
		t.Fatalf("delete result = %+v", first)
	}
	if _, err := s.ReadPayload(ctx, principal, payload.Ref); !errors.Is(err, contextstate.ErrSessionTombstoned) {
		t.Fatalf("read after delete = %v, want ErrSessionTombstoned", err)
	}
	second, err := s.DeleteSession(ctx, principal, principal.SessionID)
	if err != nil {
		t.Fatalf("repeat delete: %v", err)
	}
	if second.AuditID != first.AuditID || second.TombstoneRevision != first.TombstoneRevision {
		t.Fatalf("repeat delete = %+v, first = %+v", second, first)
	}
}

func TestExportSessionIsSanitizedAndAudited(t *testing.T) {
	ctx := context.Background()
	s, principal := openContextTestStore(t)
	defer s.Close()
	seedContextSession(t, s, principal)
	payload, event := contextSourceFixture(t, principal, "export-safe")
	if err := s.appendSourceEvents(ctx, principal, []contextstate.SourceEvent{event}, []contextstate.PayloadRecord{{Ref: payload.Ref, Retention: payload.Retention, Data: payload.Bytes}}); err != nil {
		t.Fatal(err)
	}
	export, err := s.ExportSession(ctx, principal, principal.SessionID)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	encoded := base64.StdEncoding.EncodeToString([]byte("export-safe"))
	if export.Count != 2 || export.AuditID == "" || !strings.Contains(string(export.Records), encoded) {
		t.Fatalf("export result = %+v", export)
	}
	var audits int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM context_audits WHERE action='export'`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Fatalf("export audit count = %d, want 1", audits)
	}
}

// TestExportSessionReassemblesMultiChunkPayload: multi-chunk payloads store
// NULL parent data; export must reassemble bytes (not HashOnly-drop content).
func TestExportSessionReassemblesMultiChunkPayload(t *testing.T) {
	ctx := context.Background()
	contextstate.SetLimits(contextstate.Limits{SourceEventBytes: 1024})
	t.Cleanup(func() { contextstate.SetLimits(contextstate.DefaultLimits()) })

	s, principal := openContextTestStore(t)
	defer s.Close()
	seedContextSession(t, s, principal)

	body := []byte(strings.Repeat("export-chunk-body-", 200))
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
	if err := s.appendSourceEvents(ctx, principal, []contextstate.SourceEvent{event}, []contextstate.PayloadRecord{{Ref: payload.Ref, Retention: payload.Retention, Data: payload.Bytes}}); err != nil {
		t.Fatalf("append chunked source: %v", err)
	}

	var inline []byte
	if err := s.db.QueryRow(`SELECT data FROM context_payloads WHERE ref=?`, payload.Ref.Ref).Scan(&inline); err != nil {
		t.Fatal(err)
	}
	if inline != nil {
		t.Fatalf("expected NULL inline data for multi-chunk payload, got %d bytes", len(inline))
	}

	export, err := s.ExportSession(ctx, principal, principal.SessionID)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	encoded := base64.StdEncoding.EncodeToString(body)
	if !strings.Contains(string(export.Records), encoded) {
		t.Fatalf("export missing reassembled multi-chunk payload bytes (hash_only drop?)")
	}
	// Hash-only flag must not be set when chunks reassemble successfully.
	if strings.Contains(string(export.Records), `"hash_only":true`) {
		t.Fatalf("export marked hash_only for multi-chunk payload with reassemblable data")
	}
}

func contextSourceFixture(t *testing.T, principal contextstate.Principal, value string) (contextstate.SanitizedPayload, contextstate.SourceEvent) {
	t.Helper()
	payload, err := contextstate.SanitizeSourcePayload(context.Background(), principal, []byte(value), contextstate.RedactionPolicy{Configured: true, Patterns: []string{"not-present"}})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := contextstate.NewSourceID(principal.SessionID, 1)
	event := contextstate.SourceEvent{ID: id, Kind: "message", Role: "user", PayloadRef: payload.Ref.Ref, Provenance: "host", RedactionStatus: "sanitized", Size: len(payload.Bytes)}
	return payload, event
}

// TestPruneContextPayloadsRemovesExpiredChunkedPayload: a revoked, expired
// multi-chunk payload is fully reclaimed by the prune — parent row AND chunk
// rows, atomically. Before the fix the single parent-only DELETE aborted with
// FOREIGN KEY constraint failed (context_payload_chunks.ref has no ON DELETE
// CASCADE), so revoked payload bytes were never reclaimed and the prune was
// permanently blocked.
func TestPruneContextPayloadsRemovesExpiredChunkedPayload(t *testing.T) {
	ctx := context.Background()
	contextstate.SetLimits(contextstate.Limits{SourceEventBytes: 1024})
	t.Cleanup(func() { contextstate.SetLimits(contextstate.DefaultLimits()) })

	s, principal := openContextTestStore(t)
	defer s.Close()
	seedContextSession(t, s, principal)

	body := []byte(strings.Repeat("prune-chunk-body-", 200))
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
	event := contextstate.SourceEvent{ID: eventID, Kind: "message", Role: "user", PayloadRef: payload.Ref.Ref, Provenance: "host", RedactionStatus: "sanitized", Size: payload.Ref.Size}
	if err := s.appendSourceEvents(ctx, principal, []contextstate.SourceEvent{event}, []contextstate.PayloadRecord{{Ref: payload.Ref, Retention: payload.Retention, Data: payload.Bytes}}); err != nil {
		t.Fatalf("append chunked source: %v", err)
	}
	var chunkRows int
	if err := s.db.QueryRow(`SELECT count(*) FROM context_payload_chunks WHERE ref=?`, payload.Ref.Ref).Scan(&chunkRows); err != nil {
		t.Fatal(err)
	}
	if chunkRows < 2 {
		t.Fatalf("expected multi-chunk layout, got %d chunk rows", chunkRows)
	}

	// Revoke and expire exactly as the session-deletion path leaves rows:
	// revoked=1 with an expires_at timestamp (here in the past).
	past := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
	if _, err := s.db.Exec(`UPDATE context_payloads SET revoked=1, expires_at=? WHERE ref=?`, past, payload.Ref.Ref); err != nil {
		t.Fatal(err)
	}

	got, err := s.PruneContextPayloads(ctx, time.Now(), 100)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if got != 1 {
		t.Fatalf("prune count = %d, want 1", got)
	}
	var parents, chunks int
	if err := s.db.QueryRow(`SELECT count(*) FROM context_payloads`).Scan(&parents); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM context_payload_chunks`).Scan(&chunks); err != nil {
		t.Fatal(err)
	}
	if parents != 0 || chunks != 0 {
		t.Fatalf("after prune parents=%d chunks=%d, want 0/0", parents, chunks)
	}

	again, err := s.PruneContextPayloads(ctx, time.Now(), 100)
	if err != nil {
		t.Fatalf("second prune: %v", err)
	}
	if again != 0 {
		t.Fatalf("second prune count = %d, want 0 (idempotent)", again)
	}
}

// TestPruneContextPayloadsSkipsNotYetExpiredRevoked: retention-window gating is
// pinned — a revoked payload whose expires_at is still in the future is NOT
// pruned, for both a multi-chunk and an inline layout.
func TestPruneContextPayloadsSkipsNotYetExpiredRevoked(t *testing.T) {
	ctx := context.Background()
	contextstate.SetLimits(contextstate.Limits{SourceEventBytes: 1024})
	t.Cleanup(func() { contextstate.SetLimits(contextstate.DefaultLimits()) })

	s, principal := openContextTestStore(t)
	defer s.Close()
	seedContextSession(t, s, principal)

	// Chunked payload (revoked, future expiry).
	big := []byte(strings.Repeat("prune-future-chunk-", 200))
	if len(big) <= contextstate.PayloadChunkSize() {
		t.Fatalf("fixture too small to force chunking: %d", len(big))
	}
	bigPayload, err := contextstate.SanitizeSourcePayload(ctx, principal, big, contextstate.RedactionPolicy{Configured: true, Patterns: []string{"not-present"}})
	if err != nil {
		t.Fatal(err)
	}
	bigID, _ := contextstate.NewSourceID(principal.SessionID, 1)
	bigEvent := contextstate.SourceEvent{ID: bigID, Kind: "message", Role: "user", PayloadRef: bigPayload.Ref.Ref, Provenance: "host", RedactionStatus: "sanitized", Size: bigPayload.Ref.Size}
	if err := s.appendSourceEvents(ctx, principal, []contextstate.SourceEvent{bigEvent}, []contextstate.PayloadRecord{{Ref: bigPayload.Ref, Retention: bigPayload.Retention, Data: bigPayload.Bytes}}); err != nil {
		t.Fatalf("append chunked source: %v", err)
	}

	// Inline payload (revoked, future expiry).
	smallPayload, err := contextstate.SanitizeSourcePayload(ctx, principal, []byte("prune-future-inline"), contextstate.RedactionPolicy{Configured: true, Patterns: []string{"not-present"}})
	if err != nil {
		t.Fatal(err)
	}
	smallID, _ := contextstate.NewSourceID(principal.SessionID, 2)
	smallEvent := contextstate.SourceEvent{ID: smallID, Kind: "message", Role: "user", PayloadRef: smallPayload.Ref.Ref, Provenance: "host", RedactionStatus: "sanitized", Size: smallPayload.Ref.Size}
	if err := s.appendSourceEvents(ctx, principal, []contextstate.SourceEvent{smallEvent}, []contextstate.PayloadRecord{{Ref: smallPayload.Ref, Retention: smallPayload.Retention, Data: smallPayload.Bytes}}); err != nil {
		t.Fatalf("append inline source: %v", err)
	}

	future := time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339Nano)
	for _, ref := range []string{bigPayload.Ref.Ref, smallPayload.Ref.Ref} {
		if _, err := s.db.Exec(`UPDATE context_payloads SET revoked=1, expires_at=? WHERE ref=?`, future, ref); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.PruneContextPayloads(ctx, time.Now(), 100)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if got != 0 {
		t.Fatalf("prune count = %d, want 0 (retention window not elapsed)", got)
	}
	var parents, chunks int
	if err := s.db.QueryRow(`SELECT count(*) FROM context_payloads`).Scan(&parents); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM context_payload_chunks`).Scan(&chunks); err != nil {
		t.Fatal(err)
	}
	if parents != 2 {
		t.Fatalf("parents after prune = %d, want 2 (both retained)", parents)
	}
	if chunks == 0 {
		t.Fatalf("chunk rows after prune = 0, want retained chunked payload")
	}
}
