// Package cliorchestrate provides orchestration tool implementations that
// bridge the model-facing tool set with the coordinator's async run model.
package cliorchestrate

import (
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// SessionToolRegister is wired at init time by internal/cli to
// registerSessionTool. Callers must check for nil before use; a nil value
// means the seam was not wired (test or embedding context).
var SessionToolRegister func(d *runtime.Dispatcher, reg *tools.Registry, tool tools.Tool) error
