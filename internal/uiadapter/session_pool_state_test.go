package uiadapter

import (
	"context"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

func statePoolFixture(t *testing.T) (*CommandRunner, *chat.Session, *config.Resolved) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	res := &config.Resolved{Model: "test-model", SystemPrompt: "launch baseline"}
	sess := chat.NewSession(res, noticeCompleter{})
	reg := agents.NewRegistry()
	for _, name := range []string{"alpha", "beta"} {
		if err := reg.Publish(agents.ResolvedAgent{Name: name, SystemPrompt: name + " prompt"}); err != nil {
			t.Fatal(err)
		}
	}
	state := &cliagents.AgentSessionState{Registry: reg, WorkspaceRoot: t.TempDir()}
	runner := NewCommandRunner(sess, res, state)
	runner.SetSettingsStore(NewSettingsStore(sess, res, state))
	t.Cleanup(runner.pool.CloseAll)
	return runner, sess, res
}

func selectStateAgent(t *testing.T, r *CommandRunner, name string) {
	t.Helper()
	if out := r.SelectAgent(context.Background(), name); out.Err != "" {
		t.Fatal(out.Err)
	}
}

func TestPoolEntryAgentSelectionStaysWithSession(t *testing.T) {
	r, first, _ := statePoolFixture(t)
	selectStateAgent(t, r, "alpha")
	firstState := r.agentState
	conv, err := r.pool.CreateFresh()
	if err != nil {
		t.Fatal(err)
	}
	r.SetActiveSession(r.pool.Session(conv.ID()))
	selectStateAgent(t, r, "beta")
	if firstState.DisplayName() != "alpha" {
		t.Fatalf("first session agent changed to %q", firstState.DisplayName())
	}
	if r.settingsStore.agentState != r.agentState || r.agentState == firstState {
		t.Fatal("runner and settings did not bind the private entry state")
	}
	r.SetActiveSession(first)
	if r.agentState != firstState || r.settingsStore.agentState != firstState {
		t.Fatal("switch back did not restore first entry state")
	}
}

func TestPoolEntryAgentSwitchKeepsLaunchConfig(t *testing.T) {
	r, _, res := statePoolFixture(t)
	selectStateAgent(t, r, "alpha")
	if res.SystemPrompt != "launch baseline" {
		t.Fatalf("agent switch mutated launch config: %q", res.SystemPrompt)
	}
}

func TestPoolFreshEntryRestoresLaunchBaseline(t *testing.T) {
	for _, bound := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "bound"}[bound], func(t *testing.T) {
			r, _, _ := statePoolFixture(t)
			selectStateAgent(t, r, "alpha")
			var conv ports.Conversation
			var err error
			if bound {
				conv, err = r.pool.CreateFreshBound(nil)
			} else {
				conv, err = r.pool.CreateFresh()
			}
			if err != nil {
				t.Fatal(err)
			}
			r.SetActiveSession(r.pool.Session(conv.ID()))
			if prompt, _ := r.sess.AgentSettings(); prompt != "launch baseline" {
				t.Fatalf("fresh entry inherited sibling prompt: %q", prompt)
			}
			selectStateAgent(t, r, "beta")
			selectStateAgent(t, r, config.RootAgentName)
			if prompt, _ := r.sess.AgentSettings(); prompt != "launch baseline" {
				t.Fatalf("root restored wrong baseline: %q", prompt)
			}
		})
	}
}
