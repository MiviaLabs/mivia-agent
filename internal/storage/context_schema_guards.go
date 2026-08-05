package storage

func contextSchemaGuardStatements() []string {
	return []string{
		`CREATE TRIGGER context_session_active_checkpoint_guard
            BEFORE UPDATE OF active_checkpoint_id ON context_sessions
            WHEN NEW.active_checkpoint_id IS NOT NULL AND NOT EXISTS(
                SELECT 1 FROM context_checkpoints c
                WHERE c.checkpoint_id = NEW.active_checkpoint_id
                  AND c.workspace_id = NEW.workspace_id AND c.session_id = NEW.session_id
                  AND c.subject_id = NEW.subject_id AND c.complete = 1
                  AND c.source_end <= NEW.source_sequence)
            BEGIN SELECT RAISE(ABORT, 'active checkpoint is not complete or owner scoped'); END`,
		`CREATE TRIGGER context_source_payload_guard
            BEFORE INSERT ON context_source_events
            WHEN NEW.payload_ref IS NOT NULL AND NOT EXISTS(
                SELECT 1 FROM context_payloads p
                WHERE p.ref = NEW.payload_ref AND p.namespace = COALESCE(NEW.payload_namespace, '')
                  AND p.workspace_id = NEW.workspace_id AND p.session_id = NEW.session_id
                  AND p.subject_id = NEW.subject_id AND p.revoked = 0)
            BEGIN SELECT RAISE(ABORT, 'source payload is not an active owner-scoped payload'); END`,
	}
}
