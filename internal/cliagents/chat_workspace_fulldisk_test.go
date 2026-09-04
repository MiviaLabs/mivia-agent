package cliagents

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
)

// TestConfigureChatWorkspaceWiresFullDiskReArm pins the live re-arm wiring:
// only a session whose chat workspace was configured (tools on) carries the
// hook that lets Settings -> General flip the live root; tools-off sessions
// stay persistence-only, and the hook is re-drivable (on AND off).
func TestConfigureChatWorkspaceWiresFullDiskReArm(t *testing.T) {
	root := t.TempDir()
	res := memoryTestResolved(true)
	sess := chat.NewSession(res, nil)
	state := &AgentSessionState{}

	if state.ApplyFullDisk(true) {
		t.Fatal("re-arm wired before ConfigureChatWorkspace ran")
	}
	memClose, err := ConfigureChatWorkspace(sess, root, true, res, state, false, false, false)
	if err != nil {
		t.Fatalf("ConfigureChatWorkspace: %v", err)
	}
	defer memClose()

	if !state.ApplyFullDisk(true) {
		t.Fatal("ConfigureChatWorkspace did not wire the live full-disk re-arm")
	}
	if !state.ApplyFullDisk(false) {
		t.Fatal("re-arm consumed after one call; must stay re-drivable for on/off toggles")
	}

	// Tools off: no workspace is configured, so no re-arm may exist.
	toolsOff := &AgentSessionState{}
	memClose2, err := ConfigureChatWorkspace(sess, root, false, res, toolsOff, false, false, false)
	if err != nil {
		t.Fatalf("ConfigureChatWorkspace(tools off): %v", err)
	}
	defer memClose2()
	if toolsOff.ApplyFullDisk(true) {
		t.Fatal("tools-off session wired a live full-disk re-arm")
	}
}
