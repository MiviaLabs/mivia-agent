package contextstate

import (
	"context"
	"fmt"
	"strings"
	"unicode"
)

// SessionCatalogInfo is the metadata exposed to user-facing session pickers.
// Messages remain opaque to this package and are carried as canonical bytes.
// Dir and Worktree record where the session lived; the TUI restores that
// directory when the session is opened.
type SessionCatalogInfo struct {
	SessionID    string `json:"session_id,omitempty"`
	Title        string `json:"title,omitempty"`
	Name         string `json:"name"`
	Model        string `json:"model"`
	Provider     string `json:"provider"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
	TurnCount    int    `json:"turn_count"`
	TokenCount   int    `json:"token_count"`
	MessageCount int    `json:"message_count"`
	// Dir is the absolute directory the session was created or used in.
	Dir string `json:"dir,omitempty"`
	// Worktree is the mivia worktree name when Dir lies inside one.
	Worktree string `json:"worktree,omitempty"`
	// WorktreeRoute marks a route that starts a new session in a worktree.
	// It does not contain a chat transcript or model binding.
	WorktreeRoute bool `json:"worktree_route,omitempty"`
	// WorktreeInstance retains the exact managed worktree for picker actions.
	WorktreeInstance WorktreeInstance `json:"worktree_instance,omitempty"`
}

// SessionSaveOptions carries the optional metadata written with a named
// session snapshot. The zero value is valid and records no directory.
type SessionSaveOptions struct {
	Dir              string
	Worktree         string
	WorktreeInstance WorktreeInstance
}

// MaxSessionDirBytes bounds the stored session directory string so a hostile
// or corrupt row cannot inflate every picker payload without limit.
const MaxSessionDirBytes = 4096

// MaxSessionTitleBytes bounds a title that the terminal can render safely.
const MaxSessionTitleBytes = 256

// NormalizeSessionTitle validates and trims user-facing session title text.
func NormalizeSessionTitle(title string) (string, error) {
	title = strings.TrimSpace(title)
	if len(title) > MaxSessionTitleBytes {
		return "", fmt.Errorf("%w: session title is too long", ErrInvalidDTO)
	}
	for _, r := range title {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("%w: session title contains a control character", ErrInvalidDTO)
		}
	}
	return title, nil
}

// ValidSessionDir reports whether dir is safe to persist: no NUL bytes and
// within the length bound. The empty string is valid (no directory recorded).
func ValidSessionDir(dir string) bool {
	if strings.ContainsRune(dir, '\x00') {
		return false
	}
	return len(dir) <= MaxSessionDirBytes
}

// SessionCatalog is the durable user-facing transcript surface. It is
// optional on the low-level context Store so memory/test stores need not
// implement named persistence.
type SessionCatalog interface {
	SaveSession(context.Context, Principal, string, []byte, string, string, int, int, int, SessionSaveOptions) error
	LoadSession(context.Context, Principal, string) ([]byte, SessionCatalogInfo, error)
	ListSessions(context.Context, Principal) ([]SessionCatalogInfo, error)
	DeleteSessionSnapshot(context.Context, Principal, string) error
	PruneSessionSnapshots(context.Context, Principal, []string) error
}

// SessionTitleCatalog stores optional display metadata for a bound context session.
type SessionTitleCatalog interface {
	SetSessionTitle(context.Context, Principal, string, string, WorktreeInstance) error
}

// SessionFirstMessageSource resolves the first user message of a live context
// session for display titling. It is optional: a store that does not implement
// it simply leaves sessions untitled. The lookup is subject-scoped, never
// capability-scoped, so stale-capability rows (older runs) can still be titled.
type SessionFirstMessageSource interface {
	FirstUserMessage(context.Context, Principal, string) (string, error)
}

// WorktreeRouteCatalog stores launch routes for mivia-managed worktrees.
// A route is separate from a chat session because it has no model binding.
type WorktreeRouteCatalog interface {
	SaveWorktreeRoute(context.Context, Principal, string, string) error
	DeleteWorktreeRoute(context.Context, Principal, string) error
}

// WorktreeSessionCatalog controls a managed worktree session lifecycle.
// The caller supplies the immutable instance to prevent same-name reuse.
type WorktreeSessionCatalog interface {
	BeginWorktreeCreation(context.Context, Principal, WorktreeInstance, string) error
	RegisterWorktreeInstance(context.Context, Principal, WorktreeInstance, string) error
	AbandonWorktreeCreation(context.Context, Principal, WorktreeInstance) error
	BeginWorktreeDeletion(context.Context, Principal, WorktreeInstance) error
	DeleteWorktreeSessions(context.Context, Principal, WorktreeInstance) (int, error)
	LoadWorktreeSession(context.Context, Principal, string, WorktreeInstance) ([]byte, SessionCatalogInfo, error)
	ListWorktreeSessions(context.Context, Principal, WorktreeInstance) ([]SessionCatalogInfo, error)
	DeleteWorktreeSessionSnapshot(context.Context, Principal, string, WorktreeInstance) error
	PruneWorktreeSessionSnapshots(context.Context, Principal, []string, WorktreeInstance) error
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

type WorktreeAdmissionCatalog interface {
	SaveWorktreeSessionAdmission(context.Context, Principal, string, SessionAdmission, WorktreeInstance) error
	LoadWorktreeSessionAdmission(context.Context, Principal, string, WorktreeInstance) (SessionAdmission, error)
}
