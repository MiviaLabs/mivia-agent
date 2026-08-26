package cli

import (
	cliagents "github.com/MiviaLabs/mivia-agent/internal/cliagents"
	clichat "github.com/MiviaLabs/mivia-agent/internal/clichat"
	"github.com/MiviaLabs/mivia-agent/internal/hooksession"
)

// currentHookSession, hookSessionConfigured, handleSlashHooks, and
// installHookSession are thin wrappers over internal/hooksession, which owns
// the actual session state and listing logic. They exist so the clichat and
// cliworkflow seam signatures (internal/clichat/seams.go,
// internal/cliworkflow/seams.go) do not have to change: both still wire to a
// cli-owned func with the same shape they always had.

func currentHookSession() *hooksession.Session { return hooksession.Current() }

func hookSessionConfigured() bool { return hooksession.Configured() }

// handleSlashHooks serves /hooks on the old clichat surface.
func handleSlashHooks(fields []string, term *clichat.Terminal) (bool, bool, error) {
	term.WriteString("\n" + hooksession.SlashOutput(fields))
	return true, false, nil
}

// installHookSession resolves this session's lifecycle hooks and prints the
// startup notices hooksession.Install returns. cliagents.WarnHookLoad is the
// print step; hooksession stays free of any cli dependency by not doing it
// itself.
func installHookSession(workspaceRoot string, staleBypass, quiet bool) (func(), error) {
	release, notices, err := hooksession.Install(workspaceRoot, staleBypass, quiet)
	if err != nil {
		return nil, err
	}
	cliagents.WarnHookLoad(notices)
	return release, nil
}
