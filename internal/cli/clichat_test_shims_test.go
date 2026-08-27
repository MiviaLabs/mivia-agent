package cli

// clichat_test_shims_test.go supplies test-scope shims for symbols that
// moved to internal/clichat, so characterization_test.go stays byte-identical.

import (
	cliagents "github.com/MiviaLabs/mivia-agent/internal/cliagents"
	clichat "github.com/MiviaLabs/mivia-agent/internal/clichat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// chatInvocation mirrors the moved clichat.chatInvocation fields the
// characterization suite sets.
type chatInvocation struct {
	workspacePath     string
	jsonMode, plainUI bool
	quiet             bool
}

// runConfiguredChat delegates to the moved entry point for tests.
var runConfiguredChat = func(inv chatInvocation, res *config.Resolved) error {
	return clichat.RunChatCharacterization(inv.workspacePath, inv.jsonMode, inv.plainUI, inv.quiet, res)
}

// loadChatSkills delegates to clichat.LoadChatSkills for tests.
var loadChatSkills = func(wsRoot string) (*skills.Registry, error) { return clichat.LoadChatSkills(wsRoot) }

// runAgentsWithIO delegates to cliagents.RunAgentsWithIO for tests.
var runAgentsWithIO = cliagents.RunAgentsWithIO

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

// loadAgentDefinitions delegates to cliagents.LoadAgentDefinitions for tests.
var loadAgentDefinitions = cliagents.LoadAgentDefinitions

// chatFlags delegates to the moved clichat flag parser for tests.
var chatFlags = clichat.ChatFlags

// handleSlash delegates to the moved clichat slash dispatcher for tests.
var handleSlash = clichat.HandleSlashCommand

// slashSurfaceBoth delegates to the moved clichat surface constant.
var slashSurfaceBoth = clichat.SlashSurfaceBoth

// builtInSlashCommands delegates to the moved clichat catalog for tests.
var builtInSlashCommands = clichat.BuiltInSlashCommands

// parseStackWorkflowArgs delegates to the moved clichat helper for tests.
var parseStackWorkflowArgs = clichat.ParseStackWorkflowArgsFunc

// resolveStackID delegates to the moved clichat helper for tests.
var resolveStackID = clichat.ResolveStackIDFunc

// openStackLedger delegates to the moved clichat helper for tests.
var openStackLedger = clichat.OpenStackLedgerFunc
