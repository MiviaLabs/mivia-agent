package cli

import (
	"context"
	"encoding/json"
	cliorchestrate "github.com/MiviaLabs/mivia-agent/internal/cliorchestrate"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// namelessTool is registerable (the registry does not validate names) but
// cannot be installed on a dispatcher, which is the only way to drive
// NewSessionDispatcher's tool-registration failure from outside.
type namelessTool struct{}

func (namelessTool) Name() string               { return "" }
func (namelessTool) Description() string        { return "unnameable" }
func (namelessTool) Parameters() map[string]any { return map[string]any{} }
func (namelessTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "", nil
}

// A dispatcher that cannot install its tools must fail construction rather
// than return a half-built surface: a session whose tools silently went
// missing looks identical to a session whose model chose not to call any.
func TestSessionDispatcherFailsWhenAToolCannotRegister(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(namelessTool{})
	_, err := NewSessionDispatcher(SessionDispatcherOpts{
		Registry:  reg,
		Completer: welcomeStubCompleter{},
		Model:     "m",
		Config:    config.SubagentConfig{StoreBackend: "memory"},
	})
	if err == nil {
		t.Fatal("a tool that cannot register must fail dispatcher construction")
	}
	if !strings.Contains(err.Error(), "tool dispatcher") {
		t.Fatalf("error = %v, want it to name the dispatcher stage", err)
	}
}

// Both dependencies are required; neither absence may produce a usable
// dispatcher.
func TestSessionDispatcherRejectsMissingDependencies(t *testing.T) {
	cases := map[string]SessionDispatcherOpts{
		"no registry":  {Completer: welcomeStubCompleter{}, Config: config.SubagentConfig{StoreBackend: "memory"}},
		"no completer": {Registry: tools.NewRegistry(), Config: config.SubagentConfig{StoreBackend: "memory"}},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := NewSessionDispatcher(opts); err == nil {
				t.Fatalf("%s must fail", name)
			}
		})
	}
}

// Handler names share one namespace across one-shot, multi-step, agent and
// skill registration. reservedSkillNames keeps a skill from claiming a
// built-in name at load, and this is the backstop for any registry that
// reaches the dispatcher without passing through that gate.
func TestSessionDispatcherRefusesASkillNamedLikeABuiltinHandler(t *testing.T) {
	skillReg := skills.NewRegistry()
	if err := skillReg.Register(skills.Definition{
		Name: cliorchestrate.HandlerMultiStep, Description: "collides", Instructions: "x",
	}); err != nil {
		t.Fatal(err)
	}
	_, err := NewSessionDispatcher(SessionDispatcherOpts{
		Registry:  tools.NewRegistry(),
		Completer: welcomeStubCompleter{},
		Model:     "m",
		Config:    config.SubagentConfig{StoreBackend: "memory"},
		SkillReg:  skillReg,
	})
	if err == nil {
		t.Fatal("a skill claiming a built-in handler name must fail construction")
	}
	if !strings.Contains(err.Error(), cliorchestrate.HandlerMultiStep) {
		t.Fatalf("error = %v, want it to name the colliding handler", err)
	}
}
