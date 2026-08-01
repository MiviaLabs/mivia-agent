package contextstate

import "context"

type DeleteResult struct {
	SessionID         string   `json:"session_id"`
	TombstoneRevision Revision `json:"tombstone_revision"`
	RevokedRefs       int      `json:"revoked_refs"`
	AuditID           string   `json:"audit_id"`
}

type ExportResult struct {
	SessionID string   `json:"session_id"`
	Revision  Revision `json:"revision"`
	Records   []byte   `json:"records"`
	Count     int      `json:"count"`
	AuditID   string   `json:"audit_id"`
}

type AuditRecord struct {
	ID          string `json:"id"`
	Action      string `json:"action"`
	WorkspaceID string `json:"workspace_id"`
	SessionID   string `json:"session_id"`
	SubjectID   string `json:"subject_id"`
	Revision    uint64 `json:"revision"`
	Size        int    `json:"size"`
	CreatedAt   string `json:"created_at"`
}

type SourceMapping struct {
	LegacyID    string   `json:"legacy_id"`
	SessionID   string   `json:"session_id"`
	SourceStart SourceID `json:"source_start"`
	SourceEnd   SourceID `json:"source_end"`
}

type CutoverState struct {
	Mode            string `json:"mode"`
	LegacySessionID string `json:"legacy_session_id"`
	SessionID       string `json:"session_id"`
}

type RollbackToken struct {
	SessionID      string `json:"session_id"`
	IdempotencyKey string `json:"idempotency_key"`
	Digest         string `json:"digest"`
}

type ImportResult struct {
	SessionID        string          `json:"session_id"`
	SourceRange      SourceRange     `json:"source_range"`
	Revision         Revision        `json:"revision"`
	Imported         int             `json:"imported"`
	IdempotencyKey   string          `json:"idempotency_key"`
	Status           string          `json:"status"`
	SourceMap        []SourceMapping `json:"source_map"`
	Cutover          CutoverState    `json:"cutover"`
	Rollback         RollbackToken   `json:"rollback"`
	PartialArtifacts []ContentRef    `json:"partial_artifacts"`
	Warnings         []string        `json:"warnings"`
}

type LegacyImporter interface {
	Import(context.Context, Principal, string, string) (ImportResult, error)
}

type SessionLifecycle interface {
	DeleteSession(context.Context, Principal, string) (DeleteResult, error)
	ExportSession(context.Context, Principal, string) (ExportResult, error)
}
