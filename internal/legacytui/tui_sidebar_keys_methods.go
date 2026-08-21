package legacytui

import (
	"context"
	"errors"
	"fmt"
	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"os"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/vcs"
)

func (m *TUIModel) handleSidebarKey(key string) bool {
	if m.focus != cli.FocusSidebar || !m.sidebarVisible() {
		return false
	}
	sidebar := m.sessionsSidebar
	if sidebar.confirm != confirmNone {
		switch key {
		case "y":
			if m.workspaceSwitchBusy() {
				sidebar.notice = "finish the current turn before changing sessions"
				return true
			}
			m.applySidebarSessionsConfirm()
		case "n", "esc":
			sidebar.confirm = confirmNone
		}
		return true
	}
	switch key {
	case "esc":
		m.sessionsSidebar = nil
		m.setFocus(cli.FocusComposer)
		m.layout()
		m.renderVP()
	case "up", "k":
		sidebar.move(m.sessions, -1)
	case "down", "j":
		sidebar.move(m.sessions, 1)
	case "enter":
		if sidebar.selectsNewSession(m.sessions) {
			m.startNewSession()
			break
		}
		if m.workspaceSwitchBusy() {
			m.appendInfo("(finish the current turn before opening a session)")
			break
		}
		if session, ok := sidebar.selected(m.sessions); ok {
			if err := m.openSessionInfo(session); err != nil {
				m.appendInfo("open failed: " + err.Error())
				m.renderVP()
			}
		}
	case "d":
		if m.workspaceSwitchBusy() {
			sidebar.notice = "finish the current turn before changing sessions"
			break
		}
		if session, ok := sidebar.selected(m.sessions); ok {
			if session.WorktreeRoute {
				// A broken route (one that cannot be opened) is deletable from
				// the list; a healthy worktree stays a /worktrees action.
				if openable, _ := m.worktreeRouteOpenable(session); openable {
					sidebar.notice = sessionDeleteNotice(session)
				} else {
					sidebar.confirm = confirmDeleteOne
				}
			} else {
				sidebar.confirm = confirmDeleteOne
			}
		}
	case "P":
		if m.workspaceSwitchBusy() {
			sidebar.notice = "finish the current turn before changing sessions"
			break
		}
		for _, session := range m.sessions {
			if !session.WorktreeRoute {
				sidebar.confirm = confirmPurgeAll
				break
			}
		}
	default:
		return false
	}
	return true
}

func (m *TUIModel) applySidebarSessionsConfirm() {
	sidebar := m.sessionsSidebar
	if sidebar == nil {
		return
	}
	switch sidebar.confirm {
	case confirmDeleteOne:
		session, ok := sidebar.selected(m.sessions)
		if !ok {
			break
		}
		if session.WorktreeRoute {
			// Re-check openability at confirm time: if the route became
			// openable, keep the /worktrees guidance instead of deleting.
			if openable, _ := m.worktreeRouteOpenable(session); openable {
				sidebar.notice = sessionDeleteNotice(session)
				break
			}
			if err := m.deleteWorktreeRoute(session); err != nil {
				sidebar.notice = "delete failed: " + err.Error()
				break
			}
			index := sidebar.cursor - 1
			m.sessions = append(m.sessions[:index], m.sessions[index+1:]...)
			sidebar.move(m.sessions, 0)
			sidebar.notice = fmt.Sprintf("deleted %q", session.Name)
			break
		}
		if err := m.deleteConversationGroup(session); err != nil {
			sidebar.notice = "delete failed: " + err.Error()
			break
		}
		index := sidebar.cursor - 1
		m.sessions = append(m.sessions[:index], m.sessions[index+1:]...)
		sidebar.move(m.sessions, 0)
		sidebar.notice = fmt.Sprintf("deleted %q", session.Name)
	case confirmPurgeAll:
		raw, err := m.session.ListSessions()
		if err != nil {
			sidebar.notice = "purge failed: " + err.Error()
			break
		}
		remaining := make([]chat.SessionInfo, 0, len(raw))
		deleted, failed := 0, 0
		for _, session := range raw {
			if session.WorktreeRoute {
				remaining = append(remaining, session)
				continue
			}
			if err := m.session.DeleteSession(session.Reference()); err != nil {
				failed++
				remaining = append(remaining, session)
				continue
			}
			deleted++
		}
		m.sessions = cli.CollapseConversations(remaining)
		sidebar.move(m.sessions, 0)
		if failed > 0 {
			sidebar.notice = fmt.Sprintf("purged %d sessions (%d failed)", deleted, failed)
		} else {
			sidebar.notice = fmt.Sprintf("purged %d sessions", deleted)
		}
	}
	sidebar.confirm = confirmNone
	if m.sessionSel >= len(m.sessions) {
		m.sessionSel = cli.Max(0, len(m.sessions)-1)
	}
}

// deleteWorktreeRoute removes the worktree a broken route row describes. It
// runs under the lifecycle lock through the same removal path as the CLI, so
// a broken route (missing marker, gone directory, or ghost storage rows) can
// finally be deleted from the session list.
func (m *TUIModel) deleteWorktreeRoute(si chat.SessionInfo) error {
	if m.workspaceSwitchBusy() {
		return fmt.Errorf("cannot delete while the agent is running")
	}
	root := m.resolveRepoRoot()
	worktreeConfig, err := config.LoadWorktreeConfig(root)
	if err != nil {
		return err
	}
	lock, err := cli.LockWorktreeLifecycle(root, si.Worktree)
	if err != nil {
		return err
	}
	defer lock.Close()
	store, closeStore, err := m.worktreeLifecycleStore(root)
	if err != nil {
		return err
	}
	defer closeStore()
	principal, err := cli.WorktreeRoutePrincipal(root)
	if err != nil {
		return err
	}
	if !si.WorktreeInstance.IsZero() {
		live, err := store.LiveWorktreeInstance(context.Background(), principal, si.Worktree)
		switch {
		case err == nil && live.Instance != si.WorktreeInstance:
			// A same-name replacement owns the name now. Refuse so the
			// replacement is never removed through a stale route row.
			return fmt.Errorf("worktree %q changed; refresh the session list", si.Worktree)
		case err == nil:
			// The route's instance is still live; remove it below.
		case errors.Is(err, contextstate.ErrWorktreeDeleted):
			// The instance is gone. Clean leftover rows; the stale row is
			// dropped from the list even when storage holds nothing more.
			if _, err := cli.CleanupStaleWorktreeRows(store, principal, si.Worktree); err != nil {
				return err
			}
			return nil
		default:
			return err
		}
	} else if worktree, err := vcs.Resolve(context.Background(), root, si.Worktree); err != nil {
		return err
	} else if worktree != nil {
		// A legacy route has no instance binding. A live Git worktree for
		// the name belongs to the user's /worktrees surface, never to a
		// stale session-list row.
		if _, err := os.Stat(worktree.Path); err == nil {
			return fmt.Errorf("worktree %q exists; remove it with /worktrees", si.Worktree)
		}
	}
	if _, err := cli.RemoveWorktreeLocked(root, si.Worktree, worktreeConfig.BranchPrefix, lock.File()); err != nil {
		return err
	}
	return nil
}

// handleWorkflowsSidebarKey routes keys while the /workflows sidebar has
// focus. j/k/up/down move the cursor, enter opens the run-detail dialog for
// the selected row (a no-op on an empty list: unhandled so no dialog opens
// and no command is queued), and esc closes the sidebar.
func (m *TUIModel) handleWorkflowsSidebarKey(key string) bool {
	if m.focus != cli.FocusWorkflowsSidebar || !m.workflowsSidebarVisible() {
		return false
	}
	sidebar := m.workflowsSidebar
	switch key {
	case "esc":
		m.workflowsSidebar = nil
		m.setFocus(cli.FocusComposer)
		m.layout()
		m.renderVP()
	case "up", "k":
		sidebar.move(sidebar.rows, -1)
	case "down", "j":
		sidebar.move(sidebar.rows, 1)
	case "enter":
		row, ok := sidebar.selected(sidebar.rows)
		if !ok {
			return false
		}
		m.openWorkflowRunDialog(row)
	default:
		return false
	}
	return true
}
