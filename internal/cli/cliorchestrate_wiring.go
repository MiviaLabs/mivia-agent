package cli

import (
	clichat "github.com/MiviaLabs/mivia-agent/internal/clichat"
	cliorchestrate "github.com/MiviaLabs/mivia-agent/internal/cliorchestrate"
)

func init() {
	cliorchestrate.SessionToolRegister = clichat.RegisterSessionTool
}
