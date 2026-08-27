package composition

// session_errors_test.go covers BuildSession's and
// buildSessionCheckpointStore's error branches: every guarded input that
// makes construction fail before or while the checkpoint store is opened.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

func TestBuildSessionRequiresConfig(t *testing.T) {
	_, _, _, err := BuildSession(SessionInput{})
	if err == nil || !strings.Contains(err.Error(), "config is required") {
		t.Fatalf("BuildSession without config error = %v", err)
	}
}

func TestBuildSessionRequiresStorePath(t *testing.T) {
	res := buildSessionInputConfig("http://127.0.0.1:1")
	_, _, _, err := BuildSession(SessionInput{Config: res})
	if err == nil || !strings.Contains(err.Error(), "store path is required") {
		t.Fatalf("BuildSession without store path error = %v", err)
	}
}

func TestBuildDispatcherNilRegistry(t *testing.T) {
	// BuildSession itself cannot fail in BuildDispatcher: it always
	// overwrites Dispatcher.Registry with the registry BuildRegistry just
	// built, and a nil registry is BuildDispatcher's only error. Cover the
	// branch at the BuildDispatcher seam directly.
	_, err := BuildDispatcher(DispatcherInput{})
	if err == nil || !strings.Contains(err.Error(), "create tool dispatcher") {
		t.Fatalf("BuildDispatcher(nil registry) error = %v", err)
	}
}

func TestBuildSessionOpenStoreError(t *testing.T) {
	res := buildSessionInputConfig("http://127.0.0.1:1")
	// A store path under a regular file makes the parent directory
	// creation fail inside storage.OpenSQLite.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, _, err := BuildSession(SessionInput{
		Config:    res,
		StorePath: filepath.Join(blocker, "context.db"),
	})
	if err == nil || !strings.Contains(err.Error(), "open checkpoint store") {
		t.Fatalf("BuildSession open store error = %v", err)
	}
}

func TestBuildSessionInvalidWorkspaceID(t *testing.T) {
	res := buildSessionInputConfig("http://127.0.0.1:1")
	_, _, _, err := BuildSession(SessionInput{
		Config:      res,
		StorePath:   filepath.Join(t.TempDir(), "context.db"),
		WorkspaceID: " ",
	})
	if err == nil || !strings.Contains(err.Error(), "mint checkpoint principal") {
		t.Fatalf("BuildSession invalid workspace error = %v", err)
	}
}

func TestBuildSessionBoundPrincipalSessionMismatch(t *testing.T) {
	res := buildSessionInputConfig("http://127.0.0.1:1")
	// A bound principal whose session id differs from the freshly minted
	// session's id must fail SetContextManager inside
	// buildSessionCheckpointStore.
	principal, err := contextstate.NewPrincipal("ws", "fixed-session-id", "fixed-session-id")
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, err = BuildSession(SessionInput{
		Config:    res,
		StorePath: filepath.Join(t.TempDir(), "context.db"),
		Principal: principal,
	})
	if err == nil || !strings.Contains(err.Error(), "set context manager") {
		t.Fatalf("BuildSession principal mismatch error = %v", err)
	}
}

func TestBuildSessionContextStoreCapabilityMismatch(t *testing.T) {
	res := buildSessionInputConfig("http://127.0.0.1:1")
	storePath := filepath.Join(t.TempDir(), "context.db")
	sess := chat.NewSession(res, nil)
	in := SessionInput{StorePath: storePath, WorkspaceID: "ws"}

	// Prime the store with one principal for this session.
	store, first, err := buildSessionCheckpointStore(sess, in)
	if err != nil {
		t.Fatalf("first buildSessionCheckpointStore: %v", err)
	}
	// Detach the store from the session and close it so the second call
	// opens a fresh handle against the same file.
	if err := sess.SetContextManager(nil, contextstate.Principal{}); err != nil {
		t.Fatalf("clear context manager: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	// A second principal with the same tuple but a different random
	// capability must be rejected when SetContextStore loads the session
	// the first principal wrote.
	second, err := contextstate.NewPrincipal("ws", first.SessionID, first.SubjectID)
	if err != nil {
		t.Fatal(err)
	}
	in.Principal = second
	if _, _, err := buildSessionCheckpointStore(sess, in); err == nil {
		t.Fatal("buildSessionCheckpointStore with a mismatched principal capability must fail")
	}
}
