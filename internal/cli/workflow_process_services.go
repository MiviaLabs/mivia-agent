package cli

import (
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/processservices"
)

// workflowProcessServices owns process-local workflow resources.
type workflowProcessServices struct {
	panelLimiter *controller.PanelActorLimiter
}

func processWorkflowServices() workflowProcessServices {
	return workflowProcessServices{panelLimiter: processservices.PanelLimiter()}
}
