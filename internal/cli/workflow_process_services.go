package cli

import (
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/localengine"
)

// workflowProcessServices owns process-local workflow resources.
type workflowProcessServices struct {
	panelLimiter *controller.PanelActorLimiter
}

func processWorkflowServices() workflowProcessServices {
	return workflowProcessServices{panelLimiter: localengine.PanelLimiter()}
}
