package clichat

// End-to-end total-budget regression for the routed-role-agent surface -
// the exact construction that hung in the incident: a subagent stayed
// "running" for over ten minutes after its final report was visible because
// its provider connection trickled bytes and its handler carried no total
// wall-clock budget. This test pins that a role agent run with only the
// default_total_timeout_seconds knob set is cut off by that budget, returns
// a deadline error, and stamps its done event (and result envelope)
// "timed_out".

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// hungRoleCompleter blocks every provider call until the call context's
// deadline fires - no idle gap, no error of its own: the trickle shape that
// defeats idle watchdogs.
type hungRoleCompleter struct{}

func (c *hungRoleCompleter) Name() string { return "hung-role" }

func (c *hungRoleCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

func (c *hungRoleCompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	return c.Chat(ctx, req)
}

func (c *hungRoleCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	if _, err := c.Chat(ctx, req); err != nil {
		return nil, err
	}
	return nil, context.Canceled
}

func TestRoleAgentTotalBudgetEndsRunAndStampsTimedOut(t *testing.T) {
	ws, err := workspace.Open(".")
	if err != nil {
		t.Fatal(err)
	}
	definition := agents.ResolvedAgent{Name: "researcher"}
	digest, err := definition.DefinitionDigest()
	if err != nil {
		t.Fatal(err)
	}
	agentReg := agents.NewRegistry()
	if err := agentReg.Publish(definition); err != nil {
		t.Fatal(err)
	}

	events := &eventRecorder{}
	token := SetSubagentProgress(events.record)
	defer ClearSubagentProgress(token)

	cfg := config.DefaultSubagentConfig
	// Config granularity is whole seconds, so 1 is the smallest budget the
	// knob can express; the run must still end inside a few of them.
	cfg.DefaultTotalTimeoutSec = 1
	d, err := NewSessionDispatcher(SessionDispatcherOpts{
		Registry:      tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws}),
		Completer:     &hungRoleCompleter{},
		Model:         "test-model",
		Config:        cfg,
		AgentRegistry: agentReg,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	start := time.Now()
	result := d.Invoke(context.Background(), runtime.Request{
		Kind:        runtime.Subagent,
		Name:        definition.Name,
		AgentName:   definition.Name,
		AgentDigest: digest,
		Input:       json.RawMessage(`"work"`),
	})
	if !errors.Is(result.Err, context.DeadlineExceeded) {
		t.Fatalf("result.Err = %v, want context.DeadlineExceeded from the total budget", result.Err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("run took %v; the total budget did not fire", elapsed)
	}

	snap := events.snapshot()
	var done *agent.Event
	for i := range snap {
		if snap[i].Kind == agent.EventSubagentDone {
			done = &snap[i]
		}
	}
	if done == nil {
		t.Fatal("no subagent done event emitted; a run that ends must say how it ended")
	}
	if done.Status != "timed_out" {
		t.Fatalf("done status = %q, want timed_out", done.Status)
	}
	if !strings.Contains(string(result.Output), "timed_out") {
		t.Fatalf("result envelope %s does not stamp timed_out", result.Output)
	}
}
