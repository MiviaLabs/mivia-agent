package newtui

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"github.com/MiviaLabs/mivia-agent/internal/config"
)

func TestBuildApp(t *testing.T) {
	sess := chat.NewSession(&config.Resolved{}, nil)
	res := &config.Resolved{}
	agentState := &cli.AgentSessionState{}

	appModel, err := buildApp(sess, res, true, agentState, "")
	if err != nil {
		t.Fatalf("buildApp failed: %v", err)
	}
	if appModel == nil {
		t.Fatal("expected non-nil app model")
	}
}
