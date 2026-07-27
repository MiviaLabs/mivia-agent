package subagents

import (
	"context"
	"encoding/json"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"testing"
)

type h struct{}

func (h) Invoke(context.Context, runtime.Request) (json.RawMessage, error) {
	return json.RawMessage(`{"done":true}`), nil
}
func TestPoolDependencyOrderAndDeterminism(t *testing.T) {
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "a", h{})
	_ = d.Register(runtime.Subagent, "b", h{})
	p := New(d, Policy{Workers: 2})
	got, err := p.Run(context.Background(), []Task{{ID: "b", Name: "b", DependsOn: []string{"a"}}, {ID: "a", Name: "a"}})
	if err != nil || len(got) != 2 || got[0].TaskID != "b" {
		t.Fatalf("%+v %v", got, err)
	}
}
func TestPoolRejectsCycles(t *testing.T) {
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "a", h{})
	p := New(d, Policy{})
	if _, err := p.Run(context.Background(), []Task{{ID: "a", Name: "a", DependsOn: []string{"a"}}}); err == nil {
		t.Fatal("cycle accepted")
	}
}

func TestPoolRejectsInvocationKeyCollision(t *testing.T) {
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "a", h{})
	p := New(d, Policy{})
	tasks := []Task{{ID: "a", Name: "a", InvocationKey: "same"}, {ID: "b", Name: "a", InvocationKey: "same"}}
	if _, err := p.Run(context.Background(), tasks); err == nil {
		t.Fatal("invocation key collision accepted")
	}
}

func TestPoolBlocksFailedDependenciesInPartialMode(t *testing.T) {
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "fail", handlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) { return nil, context.Canceled }))
	_ = d.Register(runtime.Subagent, "next", h{})
	p := New(d, Policy{Partial: true})
	got, err := p.Run(context.Background(), []Task{{ID: "next", Name: "next", DependsOn: []string{"fail"}}, {ID: "fail", Name: "fail"}})
	if err != nil || got[0].Status != "blocked" {
		t.Fatalf("%+v %v", got, err)
	}
}

type handlerFunc func(context.Context, runtime.Request) (json.RawMessage, error)

func (f handlerFunc) Invoke(ctx context.Context, req runtime.Request) (json.RawMessage, error) {
	return f(ctx, req)
}
