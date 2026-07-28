package runtime

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

type toolHandler struct{ r *tools.Registry }

func (h toolHandler) Invoke(ctx context.Context, req Request) (json.RawMessage, error) {
	out, err := h.r.Execute(ctx, req.Name, req.Input)
	return json.RawMessage(out), err
}
func NewToolDispatcher(r *tools.Registry, p Policy) (*Dispatcher, error) {
	if r == nil {
		return nil, fmt.Errorf("nil tool registry")
	}
	d := New(p)
	for _, t := range r.List() {
		if err := d.RegisterTool(r, t); err != nil {
			return nil, fmt.Errorf("register tool %q: %w", t.Name(), err)
		}
	}
	return d, nil
}

// RegisterTool adds a registry-backed tool to an existing dispatcher. This is
// the supported path for tools that need the dispatcher during construction
// (for example delegation tools); it keeps the model-visible registry and the
// executable dispatcher in sync.
func (d *Dispatcher) RegisterTool(r *tools.Registry, t tools.Tool) error {
	if r == nil || t == nil {
		return fmt.Errorf("invalid tool registration")
	}
	return d.Register(Tool, t.Name(), toolHandler{r: r})
}
