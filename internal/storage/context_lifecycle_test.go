package storage

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

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
