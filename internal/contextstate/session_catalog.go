package contextstate

import "context"

// SessionCatalogInfo is the metadata exposed to user-facing session pickers.
// Messages remain opaque to this package and are carried as canonical bytes.
type SessionCatalogInfo struct {
	SessionID    string `json:"session_id,omitempty"`
	Name         string `json:"name"`
	Model        string `json:"model"`
	Provider     string `json:"provider"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
	TurnCount    int    `json:"turn_count"`
	TokenCount   int    `json:"token_count"`
	MessageCount int    `json:"message_count"`
}

// SessionCatalog is the durable user-facing transcript surface. It is
// optional on the low-level context Store so memory/test stores need not
// implement named persistence.
type SessionCatalog interface {
	SaveSession(context.Context, Principal, string, []byte, string, string, int, int, int) error
	LoadSession(context.Context, Principal, string) ([]byte, SessionCatalogInfo, error)
	ListSessions(context.Context, Principal) ([]SessionCatalogInfo, error)
	DeleteSessionSnapshot(context.Context, Principal, string) error
	PruneSessionSnapshots(context.Context, Principal, []string) error
}

// SessionAdmission is a named session's deferred-tool admission record (plan
// tools/05 D3). Names are the tools admitted into the surface; Agent and Digest
// identify the agent binding and tier split they were admitted against, so a
// resume against a changed split can drop them fail-closed.
type SessionAdmission struct {
	Agent  string   `json:"agent"`
	Digest string   `json:"digest"`
	Names  []string `json:"names"`
}

// SessionAdmissionCatalog is the optional durable surface for admission
// records. A store that does not implement it simply resumes with no admitted
// tools, which is the fail-closed direction.
type SessionAdmissionCatalog interface {
	SaveSessionAdmission(context.Context, Principal, string, SessionAdmission) error
	LoadSessionAdmission(context.Context, Principal, string) (SessionAdmission, error)
}
