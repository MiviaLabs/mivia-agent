package uiadapter

import (
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// Compile-time check that CommandRunner satisfies ports.SessionMounter.
var _ ports.SessionMounter = (*CommandRunner)(nil)

// Mount satisfies ports.SessionMounter by resolving or loading a session from
// the pool without switching the active session foreground context.
func (r *CommandRunner) Mount(id string) (ports.Conversation, error) {
	if r == nil || r.pool == nil {
		return nil, fmt.Errorf("no session pool available")
	}
	return r.pool.GetOrCreate(id)
}
