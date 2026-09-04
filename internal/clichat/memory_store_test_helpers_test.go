package clichat

import (
	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/memory"
)

func openMemoryStore(root string, mc config.MemoryConfig) (memory.Store, error) {
	return cliagents.OpenMemoryStoreWithReadOnly(root, mc, false)
}

func openMemoryStoreReadOnly(root string, mc config.MemoryConfig) (memory.Store, error) {
	return cliagents.OpenMemoryStoreWithReadOnly(root, mc, true)
}
