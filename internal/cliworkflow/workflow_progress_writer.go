package cliworkflow

import (
	"encoding/json"
	"io"
	"log"
	"sync"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
)

// workflowProgressWriter is the non-interactive progress sink. It writes each
// progress event as one JSON line to the configured writer.
type workflowProgressWriter struct {
	mu sync.Mutex
	w  io.Writer
}

// Emit writes one progress event as a JSON line. A broken pipe (EPIPE) error
// is ignored so progress reporting degrades instead of killing the run.
func (p *workflowProgressWriter) Emit(e controller.ProgressEvent) {
	p.mu.Lock()
	defer p.mu.Unlock()
	_ = json.NewEncoder(p.w).Encode(e)
}

// WireCLIWorkflowProgress attaches the non-interactive JSON progress sink to
// a freshly built workflow controller. The sink is best-effort: a refusal
// disables progress reporting instead of failing the run, and a nil
// controller (resume without a body to re-run) is a no-op.
func WireCLIWorkflowProgress(built *WorkflowControllerBuild, stderr io.Writer) {
	if built == nil || built.Controller == nil || stderr == io.Discard {
		return
	}
	if err := built.Controller.SetProgressSink(&workflowProgressWriter{w: stderr}); err != nil {
		log.Printf("workflow: progress reporting disabled: %v", err)
	}
}
