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
