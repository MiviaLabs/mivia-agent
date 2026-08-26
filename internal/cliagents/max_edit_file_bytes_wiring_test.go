package cliagents

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// TestConfigureChatWorkspaceThreadsMaxEditFileBytesToTheRealRegistry is a
// regression for a gap two rounds of review flagged and a third round found
// still open: ToolsConfig.MaxEditFileBytes resolves correctly at the config
// layer (internal/config/tools_config_test.go covers that), and the field
// exists on tools.DefaultOptions and composition.RegistryInput, but
// buildWorkflowToolOpts and registryInputFromDefaultOptions - the two
// functions that actually build a real session's tool registry - never
// copied it. Every live session therefore ignored max_edit_file_bytes (and
// the max_read_bytes migration behind it) and always used the flat memory
// backstop for the in-place edit tools' file-size guard, regardless of
// config. internal/tools' own edit-guard tests construct DefaultOptions
// literals directly and could not see this: the bug lived entirely in the
// translation this package owns. This drives a real session through
// ConfigureChatWorkspace, the exact call path chat_command.go uses, and
// proves the configured cap reaches search_replace's actual refusal.
func TestConfigureChatWorkspaceThreadsMaxEditFileBytesToTheRealRegistry(t *testing.T) {
	root := t.TempDir()
	const cap = 32
	small := strings.Repeat("a", cap-1)
	big := strings.Repeat("a", cap+1)
	if err := os.WriteFile(filepath.Join(root, "small.txt"), []byte(small), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "big.txt"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}

	enabled := false
	res := &config.Resolved{
		ProviderName: "deepseek",
		Model:        "deepseek-v4-flash",
		Memory:       config.MemoryConfig{Enabled: &enabled},
		Tools:        config.ToolsConfig{MaxEditFileBytes: cap},
	}
	sess := chat.NewSession(res, nil)
	state := &AgentSessionState{}
	memClose, err := ConfigureChatWorkspace(sess, root, true, res, state, false, false, false)
	if err != nil {
		t.Fatalf("ConfigureChatWorkspace: %v", err)
	}
	defer memClose()

	replaceArgs := func(path string) json.RawMessage {
		b, _ := json.Marshal(map[string]any{"path": path, "old_string": "a", "new_string": "b", "replace_all": true})
		return b
	}

	if _, err := sess.Tools.Execute(context.Background(), "search_replace", replaceArgs("small.txt")); err != nil {
		t.Fatalf("edit under the configured cap must succeed: %v", err)
	}
	_, err = sess.Tools.Execute(context.Background(), "search_replace", replaceArgs("big.txt"))
	if err == nil {
		t.Fatal("edit over the configured max_edit_file_bytes succeeded; want a too-large refusal")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Fatalf("err = %v, want a file-too-large refusal naming the configured cap", err)
	}
}
