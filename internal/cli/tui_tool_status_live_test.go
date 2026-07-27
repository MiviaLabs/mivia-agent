package cli

import (
	"strings"
	"testing"
	"time"
)

// TDD Phase S.3: multi-tool waves must show live k/n progress on the work-status
// line (and stepDetail), not a static "Running 2 tools…" until the batch ends.
func TestIntegration_LiveToolStatus_KNProgress(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "dumb")
	m := newSmokeModel(t)
	m.mode = modeChat
	m.waiting = true
	m.turnStart = time.Now()
	m.width = 80

	// Start two tools → status collapsed "Running 2 tools…"
	m.applyToolEvents([]bridgeToolEvt{
		{Start: true, ToolCallID: "a", Name: "list_dir", Detail: `{"path":"."}`, At: time.Now()},
		{Start: true, ToolCallID: "b", Name: "grep", Detail: `{"pattern":"x"}`, At: time.Now()},
	})
	status := lastWorkStatus(m.blocks)
	if status == nil {
		t.Fatal("expected work-status block")
	}
	if !strings.Contains(status.Text, "Running 2 tools") && !strings.Contains(status.Text, "2 tools") {
		t.Fatalf("initial status=%q", status.Text)
	}

	// Complete one tool + heartbeat step → must show 1/2 done.
	m.applyToolEvents([]bridgeToolEvt{
		{Start: false, ToolCallID: "a", Name: "list_dir", Detail: "ok", At: time.Now()},
	})
	m.updateFromDrain(bridgeDrain{StepDetail: "tools 1/2 done · 2s", StepDetailAt: time.Now()})
	m.refreshLiveToolWaveStatus()

	status = lastWorkStatus(m.blocks)
	if status == nil {
		t.Fatal("status vanished")
	}
	plain := status.Text
	if !strings.Contains(plain, "1/2") && !strings.Contains(plain, "1/2 done") {
		t.Fatalf("want live k/n on status, got %q (stepDetail=%q)", plain, m.stepDetail)
	}
	if m.stepDetail != "" && !strings.Contains(m.stepDetail, "1/2") && !strings.Contains(m.stepDetail, "tools") {
		// stepDetail may be verb status; wave status must still carry k/n.
		t.Logf("stepDetail=%q (ok if status has k/n)", m.stepDetail)
	}

	// Expand still lists tools.
	status.Collapsed = false
	r := m.renderBlocksForView()
	joined := strings.Join(dumpPlain(r.Lines), "\n")
	if !strings.Contains(joined, "1/2") {
		t.Fatalf("expanded render missing k/n:\n%s", joined)
	}
}

// Heartbeat alone (no tool end yet) should still refresh k/n from open rows.
func TestIntegration_LiveToolStatus_HeartbeatOnly(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := newSmokeModel(t)
	m.mode = modeChat
	m.waiting = true
	m.turnStart = time.Now()
	m.applyToolEvents([]bridgeToolEvt{
		{Start: true, ToolCallID: "a", Name: "list_dir", Detail: `{"path":"."}`, At: time.Now()},
		{Start: true, ToolCallID: "b", Name: "grep", Detail: `{"pattern":"x"}`, At: time.Now()},
		{Start: true, ToolCallID: "c", Name: "read_file", Detail: `{"path":"a.go"}`, At: time.Now()},
	})
	// 0 done, 3 open — heartbeat reports 0/3
	m.updateFromDrain(bridgeDrain{StepDetail: "tools 0/3 done · 4s", StepDetailAt: time.Now()})
	m.refreshLiveToolWaveStatus()
	st := lastWorkStatus(m.blocks)
	if st == nil {
		t.Fatal("no status")
	}
	if !strings.Contains(st.Text, "0/3") {
		t.Fatalf("heartbeat k/n missing: %q", st.Text)
	}
	if m.stepDetail == "" {
		t.Fatal("stepDetail should hold heartbeat")
	}
}

func lastWorkStatus(blocks []ChatBlock) *ChatBlock {
	for i := len(blocks) - 1; i >= 0; i-- {
		if isWorkStatusBlock(blocks[i]) {
			return &blocks[i]
		}
	}
	return nil
}

func TestFormatLiveToolWaveSummary(t *testing.T) {
	t.Parallel()
	s := formatLiveToolWaveSummary(2, 0, 2, 3*time.Second)
	if !strings.Contains(s, "0/2") || !strings.Contains(s, "Running 2") {
		t.Fatalf("%q", s)
	}
	s = formatLiveToolWaveSummary(0, 2, 2, time.Second)
	if !strings.Contains(s, "Used 2") || !strings.Contains(s, "2/2") {
		t.Fatalf("%q", s)
	}
}
