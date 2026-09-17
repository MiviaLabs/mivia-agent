package chat

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
)

// TestChatFullDiskPersistedUserConfigFlowsToUnrestrictedWorkspace proves that
// persisted operator-owned user config ([workspace_access] full_disk = true)
// flows through chatFullDisk into an unrestricted workspace root, allowing
// outside-root path resolution.
func TestChatFullDiskPersistedUserConfigFlowsToUnrestrictedWorkspace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	userConfig := filepath.Join(home, ".mivia", "mivia.toml")
	if err := os.MkdirAll(filepath.Dir(userConfig), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userConfig, []byte("[workspace_access]\nfull_disk = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	wsDir := t.TempDir()
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "outside.txt")
	if err := os.WriteFile(outsideFile, []byte("outside content"), 0o600); err != nil {
		t.Fatal(err)
	}

	inv := chatInvocation{workspacePath: wsDir}
	if !chatFullDisk(inv, wsDir) {
		t.Fatal("chatFullDisk = false, want true from persisted user config")
	}

	res := keyedResolved()
	sess := chat.NewSession(res, nil)
	agentState := &AgentSessionState{}
	cleanup, err := configureSessionWorkspace(sess, wsDir, true, res, agentState, inv)
	if err != nil {
		t.Fatalf("configureSessionWorkspace: %v", err)
	}
	defer cleanup()

	if sess.Tools == nil {
		t.Fatal("sess.Tools is nil")
	}
	if !sess.Tools.WorkspaceUnrestricted() {
		t.Fatal("sess.Tools.WorkspaceUnrestricted() = false, want true")
	}
	out, err := sess.Tools.Execute(context.Background(), "read_file", []byte(fmt.Sprintf(`{"path":%q}`, outsideFile)))
	if err != nil {
		t.Fatalf("Execute(read_file outside): %v", err)
	}
	if !strings.Contains(string(out), "outside content") {
		t.Fatalf("Execute(read_file) = %s, want outside content", string(out))
	}
}

// TestChatFullDiskSecurityInvariantsPreserved verifies that workspace-level
// config cannot grant full-disk access and malformed/absent user config fails closed.
func TestChatFullDiskSecurityInvariantsPreserved(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	wsDir := t.TempDir()
	wsConfig := filepath.Join(wsDir, ".mivia", "mivia.toml")
	if err := os.MkdirAll(filepath.Dir(wsConfig), 0o700); err != nil {
		t.Fatal(err)
	}
	// Malicious workspace config attempting to grant full_disk
	if err := os.WriteFile(wsConfig, []byte("[workspace_access]\nfull_disk = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	inv := chatInvocation{workspacePath: wsDir}
	if chatFullDisk(inv, wsDir) {
		t.Fatal("chatFullDisk = true with only workspace config; want false (fails closed)")
	}

	// Malformed user config fails closed
	userConfig := filepath.Join(home, ".mivia", "mivia.toml")
	if err := os.MkdirAll(filepath.Dir(userConfig), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userConfig, []byte("corrupt toml [["), 0o600); err != nil {
		t.Fatal(err)
	}
	if chatFullDisk(inv, wsDir) {
		t.Fatal("chatFullDisk = true with malformed user config; want false (fails closed)")
	}
}
