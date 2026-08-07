package localengine

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/agenttools"
)

func TestInvocationRunIDIsStableAndKeyScoped(t *testing.T) {
	first := agenttools.InvocationRunID("request-1")
	if first != agenttools.InvocationRunID("request-1") {
		t.Fatal("same invocation key produced different run IDs")
	}
	if first == agenttools.InvocationRunID("request-2") {
		t.Fatal("different invocation keys produced the same run ID")
	}
	if len(first) < len("wfr-inv-")+64 || first[:len("wfr-inv-")] != "wfr-inv-" {
		t.Fatalf("invocation run ID = %q, want wfr-inv- plus a digest", first)
	}
}
