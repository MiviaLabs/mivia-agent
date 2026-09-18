package cli

// clichat_test_shims_test.go supplies test-scope shims for symbols that
// moved to internal/cli/chat, so characterization_test.go stays byte-identical.

import (
	"github.com/MiviaLabs/mivia-agent/internal/cli/agents"
	"github.com/MiviaLabs/mivia-agent/internal/cli/chat"
	workflow "github.com/MiviaLabs/mivia-agent/internal/cli/workflow"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// chatInvocation mirrors the moved chat.chatInvocation fields the
// characterization suite sets.
type chatInvocation struct {
	workspacePath     string
	jsonMode, plainUI bool
	quiet             bool
}

// runConfiguredChat delegates to the moved entry point for tests.
var runConfiguredChat = func(inv chatInvocation, res *config.Resolved) error {
	return chat.RunChatCharacterization(inv.workspacePath, inv.jsonMode, inv.plainUI, inv.quiet, res)
}

// loadChatSkills delegates to chat.LoadChatSkills for tests.
var loadChatSkills = func(wsRoot string) (*skills.Registry, error) { return chat.LoadChatSkills(wsRoot) }

// runAgentsWithIO delegates to agents.RunAgentsWithIO for tests.
var runAgentsWithIO = agents.RunAgentsWithIO

func writeCatalogAgent(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	ext := ".md"
	if strings.HasSuffix(name, ".toml") || strings.HasSuffix(name, ".md") {
		ext = ""
	}
	if err := os.WriteFile(filepath.Join(dir, name+ext), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// loadAgentDefinitions delegates to agents.LoadAgentDefinitions for tests.
var loadAgentDefinitions = agents.LoadAgentDefinitions

// chatFlags delegates to the moved clichat flag parser for tests.
var chatFlags = chat.ChatFlags

// handleSlash delegates to the moved clichat slash dispatcher for tests.
var handleSlash = chat.HandleSlashCommand

// slashSurfaceBoth delegates to the moved clichat surface constant.
var slashSurfaceBoth = chat.SlashSurfaceBoth

// builtInSlashCommands delegates to the moved clichat catalog for tests.
var builtInSlashCommands = chat.BuiltInSlashCommands

// ParseStackWorkflowArgs delegates to the moved workflow helper for tests.
var parseStackWorkflowArgs = workflow.ParseStackWorkflowArgs

// resolveStackID delegates to the moved workflow helper for tests.
var resolveStackID = workflow.ResolveStackID

// openStackLedger delegates to the moved workflow helper for tests.
var openStackLedger = workflow.OpenStackLedger
