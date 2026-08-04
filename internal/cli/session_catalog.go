package cli

import (
	"path/filepath"
	"sort"
	"strconv"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
)

// listSessions merges local sessions with the main repository catalog.
func (m *tuiModel) listSessions() ([]chat.SessionInfo, error) {
	infos, err := m.session.ListSessions()
	if err != nil || m.worktreeRouteRoot == "" {
		return infos, err
	}
	repositorySessions, repositoryErr := listRepositorySessions(m.worktreeRouteRoot, m.repositorySessionStorePath)
	seen := make(map[string]struct{}, len(infos))
	for _, info := range infos {
		seen[sessionCatalogKey(info)] = struct{}{}
	}
	for _, session := range repositorySessions {
		key := sessionCatalogKey(session)
		if _, ok := seen[key]; ok {
			continue
		}
		if !session.WorktreeRoute && !sameSessionWorkspace(m.resolveWorkspaceDir(), m.worktreeRouteRoot) {
			session.ResumeWorkspace = m.worktreeRouteRoot
		}
		infos = append(infos, session)
		seen[key] = struct{}{}
	}
	routes, routesErr := listWorktreeRoutes(m.worktreeRouteRoot)
	for _, route := range routes {
		key := sessionCatalogKey(route)
		if _, ok := seen[key]; ok {
			continue
		}
		infos = append(infos, route)
		seen[key] = struct{}{}
	}
	sort.SliceStable(infos, func(i, j int) bool {
		return infos[i].UpdatedAt.After(infos[j].UpdatedAt)
	})
	if repositoryErr != nil {
		return infos, repositoryErr
	}
	return infos, routesErr
}

func sessionCatalogKey(info chat.SessionInfo) string {
	return info.Name + "\x00" + info.Dir + "\x00" + strconv.FormatBool(info.WorktreeRoute)
}

func sameSessionWorkspace(a, b string) bool {
	a, errA := filepath.Abs(a)
	b, errB := filepath.Abs(b)
	return errA == nil && errB == nil && filepath.Clean(a) == filepath.Clean(b)
}
