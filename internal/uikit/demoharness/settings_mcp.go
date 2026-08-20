package demoharness

import (
	"context"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// harnessMCP is the ports.MCPSettings adapter.
type harnessMCP struct{ *Harness }

func (m harnessMCP) MCPServers() []ports.MCPServerView {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ports.MCPServerView, len(m.settingsMCP))
	copy(out, m.settingsMCP)
	return out
}

func (m harnessMCP) Apply(_ context.Context, _ ports.Scope, e ports.MCPEdit) (ports.SaveHandle, error) {
	return m.newSaveHandle(func() error { return m.applyMCP(e) }), nil
}

func (h *Harness) findMCPServer(id string) int {
	for i := range h.settingsMCP {
		if h.settingsMCP[i].ID == id {
			return i
		}
	}
	return -1
}

func (h *Harness) applyMCP(e ports.MCPEdit) error {
	switch v := e.(type) {
	case ports.UpsertMCPServer:
		if i := h.findMCPServer(v.Server.ID); i >= 0 {
			h.settingsMCP[i] = v.Server
			return nil
		}
		h.settingsMCP = append(h.settingsMCP, v.Server)
	case ports.RemoveMCPServer:
		i := h.findMCPServer(v.ID)
		if i < 0 {
			return fmt.Errorf("mcp server %q not found", v.ID)
		}
		h.settingsMCP = append(h.settingsMCP[:i], h.settingsMCP[i+1:]...)
	case ports.SetMCPServerEnabled:
		i := h.findMCPServer(v.ID)
		if i < 0 {
			return fmt.Errorf("mcp server %q not found", v.ID)
		}
		// The fake honours the flip; internal/mcp has no runtime
		// enable/disable API today, so the real adapter's status badge
		// stays MCPStateUnknown until it grows one (settings-screen.md
		// §11).
		h.settingsMCP[i].Enabled = v.On
		h.settingsMCP[i].State = ports.MCPStateUnknown
	default:
		return fmt.Errorf("unknown mcp edit %T", e)
	}
	return nil
}
