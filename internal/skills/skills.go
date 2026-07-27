// Package skills defines independently typed, policy-bearing skills.
package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"time"
)

type Definition struct {
	Name                      string
	Version                   string
	Scope                     string
	Permission                string
	Timeout                   time.Duration
	Budget                    int
	InputSchema, OutputSchema map[string]any
	Tools                     []string
	Run                       func(context.Context, json.RawMessage) (json.RawMessage, error)
}
type Registry struct{ items map[string]Definition }

func NewRegistry() *Registry { return &Registry{items: map[string]Definition{}} }
func (r *Registry) Register(d Definition) error {
	if d.Name == "" || d.Run == nil {
		return fmt.Errorf("invalid skill")
	}
	if _, ok := r.items[d.Name]; ok {
		return fmt.Errorf("duplicate skill %q", d.Name)
	}
	r.items[d.Name] = d
	return nil
}
func (r *Registry) Get(name string) (Definition, bool) { d, ok := r.items[name]; return d, ok }
func (r *Registry) Handler(name string) (runtime.Handler, error) {
	d, ok := r.Get(name)
	if !ok {
		return nil, fmt.Errorf("unknown skill %q", name)
	}
	return handler{d}, nil
}

type handler struct{ d Definition }

func (h handler) Invoke(ctx context.Context, req runtime.Request) (json.RawMessage, error) {
	return h.d.Run(ctx, req.Input)
}
func (r *Registry) RegisterAll(d *runtime.Dispatcher) error {
	for _, s := range r.items {
		h, _ := r.Handler(s.Name)
		if err := d.Register(runtime.Skill, s.Name, h); err != nil {
			return err
		}
	}
	return nil
}
