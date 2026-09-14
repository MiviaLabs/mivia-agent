package chat

import (
	"github.com/MiviaLabs/mivia-agent/internal/cli/agents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/memory"
)

func openMemoryStore(root string, mc config.MemoryConfig) (memory.Store, error) {
	return agents.OpenMemoryStoreWithReadOnly(root, mc, false)
}

func openMemoryStoreReadOnly(root string, mc config.MemoryConfig) (memory.Store, error) {
	return agents.OpenMemoryStoreWithReadOnly(root, mc, true)
}
