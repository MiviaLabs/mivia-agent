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
