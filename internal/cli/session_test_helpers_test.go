package cli

import (
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
)

func newTestSessionForModel(model string) *chat.Session {
	return chat.NewSession(&config.Resolved{Model: model}, nil)
}
