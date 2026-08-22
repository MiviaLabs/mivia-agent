package cli

import cliorchestrate "github.com/MiviaLabs/mivia-agent/internal/cliorchestrate"

func init() {
	cliorchestrate.SessionToolRegister = registerSessionTool
}
