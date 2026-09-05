package cliagents

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
)

func TestConfigureChatWorkspaceReportsLiveAccess(t *testing.T) {
	res := memoryTestResolved(true)
	sess := chat.NewSession(res, nil)
	state := &AgentSessionState{}
	closeFn, err := ConfigureChatWorkspace(sess, t.TempDir(), true, res, state, false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	for _, on := range []bool{true, false} {
		if !state.ApplyFullDisk(on) {
			t.Fatal("live access callback is absent")
		}
		if got := sess.Tools.WorkspaceUnrestricted(); got != on {
			t.Errorf("registry access = %v, want %v", got, on)
		}
	}
}
