package runtime

import (
	"context"
	"encoding/json"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

type toolHandler struct{ r *tools.Registry }

func (h toolHandler) Invoke(ctx context.Context, req Request) (json.RawMessage, error) {
	out, err := h.r.Execute(ctx, req.Name, req.Input)
	return json.RawMessage(out), err
}
func NewToolDispatcher(r *tools.Registry, p Policy) *Dispatcher {
	d := New(p)
	for _, t := range r.List() {
		_ = d.Register(Tool, t.Name(), toolHandler{r: r})
	}
	return d
}
