package clichat

import (
	"context"
	"fmt"
	"io"
	"os"

	cliagents "github.com/MiviaLabs/mivia-agent/internal/cliagents"
)

// runCompact drives `mivia compact --session <name> [--json] [--workspace
// dir]`, a standalone way to shrink a stored session's context outside an
// interactive chat. It reuses the same catalog-session plumbing "sessions
// usage" does (newCatalogSessionAt/configureChatWorkspace), but loads the
// session writably (chat.Session.Load, not LoadReadOnly) since
// chat.Session.CompactWithResult durably commits the compacted state.
func runCompact(args []string) error {
	return runCompactWithIO(args, os.Stdout)
}

func runCompactWithIO(args []string, stdout io.Writer) error {
	session, rest, found, err := FlagValueFunc(args, "--session")
	if err != nil {
		return fmt.Errorf("compact: %w", err)
	}
	if !found || session == "" {
		return fmt.Errorf("compact: --session <name> is required")
	}
	workspaceRoot, jsonFlag, _, err := parseSessionsWorkspaceAndJSON("compact", rest, 0)
	if err != nil {
		return err
	}

	sess, store, root, res, err := newCatalogSessionAt(workspaceRoot)
	if err != nil {
		return fmt.Errorf("compact: %w", err)
	}
	defer store.Close()
	if err := sess.Load(session); err != nil {
		return fmt.Errorf("compact: %w", err)
	}
	// runRecoverySweep=false (F14): a standalone compaction is not a session
	// start and must not push branches, publish PRs, or drive stacks.
	cleanup, err := cliagents.ConfigureChatWorkspace(sess, root, true, res, &AgentSessionState{}, true, false, false)
	defer cleanup()
	if err != nil {
		return fmt.Errorf("compact: %w", err)
	}

	preparation, err := sess.CompactWithResult(context.Background(), "")
	if err != nil {
		return fmt.Errorf("compact: %w", err)
	}

	if jsonFlag {
		return writeSessionsJSON(stdout, map[string]any{
			"session":         session,
			"before_tokens":   preparation.BeforeTokens,
			"after_tokens":    preparation.AfterTokens,
			"elided_messages": preparation.ElidedMessages,
			"elided_bytes":    preparation.ElidedBytes,
		})
	}
	fmt.Fprintf(stdout, "compacted session %q: %d -> %d tokens (%d messages elided, %d bytes)\n",
		session, preparation.BeforeTokens, preparation.AfterTokens,
		preparation.ElidedMessages, preparation.ElidedBytes)
	return nil
}
