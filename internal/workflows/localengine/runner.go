package localengine

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
)

// StaticStepRunner returns fixed JSON for every agent step (scripted tests).
type StaticStepRunner struct {
	Output     json.RawMessage
	ByStep     map[string]json.RawMessage
	BlockUntil <-chan struct{}
	OnStep     func(controller.AgentStepRequest)
}

// RunStep implements controller.AgentStepRunner.
func (r *StaticStepRunner) RunStep(ctx context.Context, req controller.AgentStepRequest) (controller.AgentStepResult, error) {
	if r.OnStep != nil {
		r.OnStep(req)
	}
	if r.BlockUntil != nil {
		select {
		case <-ctx.Done():
			return controller.AgentStepResult{}, ctx.Err()
		case <-r.BlockUntil:
		}
	}
	out := r.Output
	if r.ByStep != nil {
		if v, ok := r.ByStep[req.StepID]; ok {
			out = v
		}
	}
	if len(out) == 0 {
		out = json.RawMessage(`{"ok":true}`)
	}
	return controller.AgentStepResult{
		CoordinatorRunID: "coord-" + req.StepID + "-" + fmt.Sprint(req.AttemptNo),
		TaskID:           req.TaskID,
		Output:           out,
		EvidenceJSON:     []byte(`[]`),
	}, nil
}
