package agents

import (
	"slices"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

func TestResolveMCPServersScopesRootAndChild(t *testing.T) {
	cfg := config.MCPConfig{Enabled: true, Servers: []config.MCPServerConfig{
		{ID: "global", Global: true}, {ID: "private", Global: false},
	}}
	global, err := resolveMCPServers(ResolveInput{Name: "root", Source: config.AgentSourceWorkspace}, nil, cfg)
	if err != nil || !slices.Equal(global, []string{"global"}) {
		t.Fatalf("root default = %v, %v; want [global], nil", global, err)
	}
	userIDs := []string{"private"}
	user, err := resolveMCPServers(ResolveInput{Name: "user", Source: config.AgentSourceUser, Spec: config.AgentFileSpec{MCPServers: &userIDs}}, nil, cfg)
	if err != nil || !slices.Equal(user, userIDs) {
		t.Fatalf("user selection = %v, %v; want %v, nil", user, err, userIDs)
	}
	empty := []string{}
	child, err := resolveMCPServers(ResolveInput{Name: "child", Source: config.AgentSourceWorkspace, Spec: config.AgentFileSpec{MCPServers: &empty}}, &ResolvedAgent{EffectiveMCPServers: global}, cfg)
	if err != nil || len(child) != 0 {
		t.Fatalf("child empty selection = %v, %v; want none, nil", child, err)
	}
}

func TestResolveMCPServersWorkspaceRootCannotSelectPrivate(t *testing.T) {
	ids := []string{"private"}
	_, err := resolveMCPServers(ResolveInput{
		Name: "workspace", Source: config.AgentSourceWorkspace,
		Spec: config.AgentFileSpec{MCPServers: &ids},
	}, nil, config.MCPConfig{Enabled: true, Servers: []config.MCPServerConfig{{ID: "private"}}})
	if err == nil {
		t.Fatal("workspace root selected a non-global MCP server")
	}
}

// Resolve must actually populate EffectiveDenylist, or every consumer of it
// silently enforces nothing.
//
// The field is what carries a denial to producers that add tool names AFTER
// applyToolPolicy has run - cliagents.AuthorizedAgentTools grants authority
// over a selected MCP server's whole tool set - and several of those run where
// the operator's config is not reachable. An unset field is a denylist that
// exists in the config and nowhere else.
func TestResolveCarriesTheOperatorDenylistOnTheAgent(t *testing.T) {
	opts := baseOpts()
	opts.Global.MandatoryToolDenylistAdditions = []string{"run_command"}
	reg, _, err := ResolveAll([]ResolveInput{{
		Name: "worker", Source: config.AgentSourceUser,
		Spec: config.AgentFileSpec{
			Name: strp("worker"), Description: strp("d"),
			Tools:           slicep("read_file", "run_command"),
			DisallowedTools: slicep("post_message"),
		},
	}}, opts)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	agent, ok := reg.Get("worker")
	if !ok {
		t.Fatal("worker did not resolve")
	}
	if !slicesContain(agent.EffectiveDenylist, "run_command") {
		t.Errorf("EffectiveDenylist = %v, missing the operator's addition; every "+
			"consumer of it then enforces nothing", agent.EffectiveDenylist)
	}
	if !slicesContain(agent.EffectiveDenylist, "post_message") {
		t.Errorf("EffectiveDenylist = %v, missing the agent's own "+
			"disallowed_tools", agent.EffectiveDenylist)
	}
}

func slicesContain(s []string, want string) bool {
	for _, v := range s {
		if v == want {
			return true
		}
	}
	return false
}
