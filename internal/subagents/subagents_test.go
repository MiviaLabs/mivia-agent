package subagents

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/runtime"
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

func TestPoolRejectsNegativeBudgetBeforeCumulativeAccounting(t *testing.T) {
	d := runtime.New(runtime.Policy{})
	p := New(d, Policy{MaxBudget: 3})

	_, err := p.validate([]Task{
		{ID: "negative", Budget: -2},
		{ID: "positive", Budget: 3},
		{ID: "overflow", Budget: 1},
	})
	if err == nil || err.Error() != "budget must be non-negative" {
		t.Fatalf("validate error = %v, want negative-budget rejection", err)
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

func TestPoolTaskTimeoutSurfacesTimedOutStatus(t *testing.T) {
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "slow", handlerFunc(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		select {
		case <-time.After(time.Second):
			return json.RawMessage(`{"ok":true}`), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}))
	p := New(d, Policy{Workers: 1})
	start := time.Now()
	got, err := p.Run(context.Background(), []Task{{
		ID: "t1", Name: "slow", Timeout: 30 * time.Millisecond,
	}})
	if elapsed := time.Since(start); elapsed > 400*time.Millisecond {
		t.Fatalf("pool hang: %s", elapsed)
	}
	if err != nil {
		t.Fatalf("unexpected run err: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("results=%d", len(got))
	}
	if got[0].Status != "timed_out" {
		t.Fatalf("status=%q want timed_out err=%v", got[0].Status, got[0].Err)
	}
	if got[0].Err == nil {
		t.Fatal("expected error on timed_out task")
	}
}

func TestPoolCancelReturnsPartialResults(t *testing.T) {
	d := runtime.New(runtime.Policy{})
	blockStarted := make(chan struct{})
	fastDone := make(chan struct{})
	_ = d.Register(runtime.Subagent, "block", handlerFunc(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		close(blockStarted)
		<-ctx.Done()
		return nil, ctx.Err()
	}))
	_ = d.Register(runtime.Subagent, "fast", handlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) {
		close(fastDone)
		return json.RawMessage(`{"done":true}`), nil
	}))
	p := New(d, Policy{Workers: 2, Partial: true})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan []Result, 1)
	go func() {
		got, _ := p.Run(ctx, []Task{
			{ID: "a", Name: "fast"},
			{ID: "b", Name: "block"},
		})
		done <- got
	}()
	// Wait for both handlers (channel sync, no sleep).
	for _, ch := range []<-chan struct{}{blockStarted, fastDone} {
		select {
		case <-ch:
		case <-time.After(time.Second):
			t.Fatal("handler did not start/finish")
		}
	}
	cancel()
	select {
	case got := <-done:
		by := map[string]Result{}
		for _, r := range got {
			by[r.TaskID] = r
		}
		if by["a"].Status != "completed" {
			t.Fatalf("fast task status=%q want completed", by["a"].Status)
		}
		if by["b"].Status != "canceled" && by["b"].Status != "timed_out" && by["b"].Status != "failed" {
			t.Fatalf("block task status=%q want canceled-ish", by["b"].Status)
		}
	case <-time.After(time.Second):
		t.Fatal("pool.Run did not return after cancel")
	}
}

func TestPoolPolicyTimeoutFallback(t *testing.T) {
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "slow", handlerFunc(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		select {
		case <-time.After(time.Second):
			return json.RawMessage(`{}`), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}))
	p := New(d, Policy{Workers: 1, Timeout: 25 * time.Millisecond})
	start := time.Now()
	got, err := p.Run(context.Background(), []Task{{ID: "t1", Name: "slow"}}) // no per-task timeout
	if elapsed := time.Since(start); elapsed > 400*time.Millisecond {
		t.Fatalf("hang: %s", elapsed)
	}
	if err != nil {
		t.Fatalf("run err: %v", err)
	}
	if got[0].Status != "timed_out" {
		t.Fatalf("status=%q err=%v", got[0].Status, got[0].Err)
	}
	if !strings.Contains(got[0].Err.Error(), "deadline") && got[0].Err != context.DeadlineExceeded {
		// status is enough; err should be present
		if got[0].Err == nil {
			t.Fatal("expected timeout error")
		}
	}
}
