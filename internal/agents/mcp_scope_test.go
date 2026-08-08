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
